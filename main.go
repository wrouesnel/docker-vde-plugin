package main

import (
	"errors"
	"os/exec"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/wrouesnel/go.log"

	"flag"
	"fmt"
	"github.com/wrouesnel/docker-vde-plugin/fsutil"
	"github.com/ziutek/utils/netaddr"
	"gopkg.in/alecthomas/kingpin.v2"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
)

const (
	PluginName string = "vde"
)

const (
	InterfacePrefix string = "vde"
)

// Option parameters we recognize for networks
const (
	// Specifies an existing VDE switch to associate with a network
	NetworkOptionSwitchSocket string = "socket_dir"
	// Specifies the existing VDE switches management socket.
	// Can be left out (management options will not work from the driver)
	NetworkOptionSwitchManagementSocket string = "management_socket"
	// Specifies that if the supplied sockets do not exist, they should be
	// used as the paths for a new vde_switch for the network.
	NetworkOptionsAllowCreate string = "create_sockets"
	// Specify the group owner for the created socket.
	NetworkOptionsSocketGroup string = "socket_group"
)

type VDENetworkEndpoint struct {
	// vde_plug2tap cmd. nil if no container has actually attached yet.
	tapPlugCmd *exec.Cmd
	tapCmdPipe io.WriteCloser
	// IPv4 address if assigned
	address    net.IP
	addressNet net.IPNet
	// IPv6 address if assigned
	address6    net.IP
	addressNet6 net.IPNet
	// Hardware address (always assigned)
	macAddress net.HardwareAddr
	// Gateways
	gateway  net.IP
	gateway6 net.IP
	// Current tap device. Empty means no tap currently instantiated.
	tapDevName string
}

// Hard terminate the tap command feeding data to the tap interface, if it's
// runnning.
func (this *VDENetworkEndpoint) KillTapCmd() {
	if this.tapCmdPipe != nil {
		this.tapCmdPipe.Close()
		this.tapCmdPipe = nil
	}

	if this.tapPlugCmd == nil {
		return
	}

	// Kill and collect status
	this.tapPlugCmd.Process.Kill()
	this.tapPlugCmd.Wait()
	this.tapPlugCmd = nil
}

func (this *VDENetworkEndpoint) DeleteTapDevice() {
	if this.tapDevName == "" {
		return
	}
	err := fsutil.CheckExec("ip", "link", "delete", "dev", this.tapDevName)
	// Remove the interface
	if err != nil {
		log.Errorln("Error removing tap device:", this.tapDevName)
	}
	this.tapDevName = ""
}

func (this *VDENetworkEndpoint) GetIPv4Gateway() string {
	if this.gateway == nil {
		return ""
	}
	return this.gateway.String()
}

func (this *VDENetworkEndpoint) GetIPv6Gateway() string {
	if this.gateway6 == nil {
		return ""
	}
	return this.gateway6.String()
}

// Returns a stringified CIDR-notation address for the IPv4 address of the endpoint
func (this *VDENetworkEndpoint) GetIPv4CIDRAddress() string {
	if this.address == nil {
		return ""
	}

	if this.address.IsUnspecified() {
		return ""
	}

	suffix, _ := this.addressNet.Mask.Size()
	// Get a stringified CIDR address
	return this.address.String() + "/" + fmt.Sprintf("%d", suffix)
}

// Returns a stringified CIDR-notation address for the IPv6 address of the endpoint
func (this *VDENetworkEndpoint) GetIPv6CIDRAddress() string {
	if this.address6 == nil {
		return ""
	}

	if this.address6.IsUnspecified() {
		return ""
	}

	suffix, _ := this.addressNet6.Mask.Size()
	// Get a stringified CIDR address
	return this.address6.String() + "/" + fmt.Sprintf("%d", suffix)
}

// Returns a stringified MAC address
func (this *VDENetworkEndpoint) GetMACAddress() string {
	return this.macAddress.String()
}

type VDENetworkEndpoints map[string]*VDENetworkEndpoint

// Represents a golang formatted IPAM network pool
type IPAMNetworkPool struct {
	addressSpace string
	pool         net.IPNet
	gateway      net.IP
}

// Returns a driver IPAMNetworkPool
func NewIPAMNetworkPool(inp *network.IPAMData) (IPAMNetworkPool, error) {
	_, poolNetwork, err := net.ParseCIDR(inp.Pool)
	if err != nil {
		log.Errorln("Supplied CIDR is unparseable:", inp.Pool)
		return IPAMNetworkPool{}, errors.New("Could not parse IPAM address pool")
	}

	if poolNetwork == nil {
		log.Errorln("Supplied CIDR did not include a network", inp.Pool)
		return IPAMNetworkPool{}, errors.New("Could not parse IPAM address pool")
	}

	ip, _, err := net.ParseCIDR(inp.Gateway)
	if err != nil {
		log.Errorln("Supplied Gateway was unparseable", inp.Gateway)
		return IPAMNetworkPool{}, errors.New("Could not parse IPAM gateway")
	}

	return IPAMNetworkPool{
		addressSpace: inp.AddressSpace,
		pool:         *poolNetwork,
		gateway:      ip,
	}, nil
}

type VDENetworkDesc struct {
	sockDir  string
	mgmtSock string
	mgmtPipe io.WriteCloser
	// vde_switch process
	switchp *exec.Cmd
	// IPAM data for this network
	pool4 []IPAMNetworkPool
	pool6 []IPAMNetworkPool
	// Currently executed vde_plug2tap processes
	networkEndpoints VDENetworkEndpoints
	// Mutex for networkEndpoints
	mtx sync.RWMutex
}

// For a given IP, find a suitable gateway IP in the current network. Return nil
// if nothing suitable is found.
func (this *VDENetworkDesc) GetGateway(ip net.IP) net.IP {
	var searchPool []IPAMNetworkPool
	if ip.To4() != nil {
		// IPv4 address
		searchPool = this.pool4
	} else {
		// IPv6 address
		searchPool = this.pool6
	}

	// Check if our IP is contained in the subpool
	for _, subpool := range searchPool {
		if subpool.pool.Contains(ip) {
			return subpool.gateway
		}
	}

	return nil
}

// Get a free IP. Not safe unless the network is locked while doing so.
// Returns nil if no IP can be found. Does not account for other IPs which
// may be on this network.
func (this *VDENetworkDesc) GetFreeIPv4() net.IP {
	// This is inefficient and should be abstracted to a global hash in future
	usedIPv4 := make(map[string]interface{})
	for _, endpoint := range this.networkEndpoints {
		usedIPv4[endpoint.address.String()] = nil
	}

	// Probe the map with incrementing IPs still we find one we can use
	for _, pool := range this.pool4 {
		for ip := pool.pool.IP.Mask(pool.pool.Mask); pool.pool.Contains(ip); netaddr.IPAdd(ip, 1) {
			if _, found := usedIPv4[ip.String()]; !found {
				return ip
			}
		}
	}

	return nil
}

// Get a free IP. Not safe unless the network is locked while doing so.
// Returns nil if no IP can be found. Does not account for other IPs which
// may be on this network.
func (this *VDENetworkDesc) GetFreeIPv6() net.IP {
	// This is inefficient and should be abstracted to a global hash in future
	usedIPv6 := make(map[string]interface{})
	for _, endpoint := range this.networkEndpoints {
		usedIPv6[endpoint.address.String()] = nil
	}

	// Probe the map with incrementing IPs still we find one we can use
	for _, pool := range this.pool6 {
		for ip := pool.pool.IP.Mask(pool.pool.Mask); pool.pool.Contains(ip); netaddr.IPAdd(ip, 1) {
			if _, found := usedIPv6[ip.String()]; !found {
				return ip
			}
		}
	}

	return nil
}

func (this *VDENetworkDesc) EndpointExists(endpointId string) bool {
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	_, found := this.networkEndpoints[endpointId]
	return found
}

// Implements network.Driver
type VDENetworkDriver struct {
	// Socket root directory for managing connections
	socketRoot string
	// Currently managed networks
	networks map[string]*VDENetworkDesc
	mtx      sync.RWMutex
}

// Consistently shorten a network ID to something manageable by vde_switch,
// which has problems with the very long path names generated.
func shortenNetworkId(networkId string) string {
	// vde_switch has an internal limit on the length of this name from the
	// command line it seems. Base64 encoding isn't really enough to beat it,
	// so we truncate to docker cli length for convenience and ensure our own
	// commands can handle conflicts.
	return networkId[:12]
}

func (this *VDENetworkDriver) getNetworkSocketDirName(networkId string) string {
	ctlDirName := filepath.Join(this.socketRoot, shortenNetworkId(networkId))
	return ctlDirName
}

func (this *VDENetworkDriver) networkExists(networkId string) bool {
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	_, found := this.networks[networkId]
	return found
}

func (this *VDENetworkDriver) GetCapabilities() (*network.CapabilitiesResponse, error) {
	// Technically, VDE can be global, but we have no way to know that.
	return &network.CapabilitiesResponse{Scope: network.LocalScope}, nil
}

func (this *VDENetworkDriver) CreateNetwork(req *network.CreateNetworkRequest) error {
	log := log.With("NetworkID", req.NetworkID)

	// Log a lot of information about what's happening since it's useful for debugging
	log.Infoln("CreateNetwork request received")
	for _, ipData := range req.IPv4Data {
		log.With("AddressSpace", ipData.AddressSpace).
			With("Gateway", ipData.Gateway).
			With("Pool", ipData.Pool).Debugln("IPv4 Network Options")
	}
	for _, ipData := range req.IPv6Data {
		log.With("AddressSpace", ipData.AddressSpace).
			With("Gateway", ipData.Gateway).
			With("Pool", ipData.Pool).Debugln("IPv6 Network Options")
	}
	netOptionsLogs := log
	for k, v := range req.Options {
		netOptionsLogs = netOptionsLogs.With(k, v)
	}
	netOptionsLogs.Debugln("Network options")
	// Check our state at a high level to see if we can make a new network.
	if this.networkExists(req.NetworkID) {
		return errors.New("Network already exists.")
	}

	// Parse network creation options
	var socketName string
	var managementSocketName string
	var createSockets string
	var socketGroup string
	if req.Options != nil {
		if req.Options["com.docker.network.generic"] != nil {
			dockerCliOptions := req.Options["com.docker.network.generic"].(map[string]interface{})
			socketName, _ = dockerCliOptions[NetworkOptionSwitchSocket].(string)
			managementSocketName, _ = dockerCliOptions[NetworkOptionSwitchManagementSocket].(string)
			createSockets, _ = dockerCliOptions[NetworkOptionsAllowCreate].(string)
			socketGroup, _ = dockerCliOptions[NetworkOptionsSocketGroup].(string)
		}
	}

	pool4 := make([]IPAMNetworkPool, 0)
	pool6 := make([]IPAMNetworkPool, 0)

	// Parse network IP data.
	for _, ipampool := range req.IPv4Data {
		driverPool, err := NewIPAMNetworkPool(ipampool)
		if err != nil {
			return err
		}
		pool4 = append(pool4, driverPool)
	}

	for _, ipampool := range req.IPv6Data {
		driverPool, err := NewIPAMNetworkPool(ipampool)
		if err != nil {
			return err
		}
		pool6 = append(pool6, driverPool)
	}

	// There's a few options here:
	// - make a socket in the default location
	// - use an existing named socket
	// - create a socket in a specified location

	if socketName == "" {
		incrementNumber := 0
		baseSocketName := this.getNetworkSocketDirName(req.NetworkID)
		socketName = baseSocketName
		for fsutil.PathExists(socketName) {
			socketName = baseSocketName + fmt.Sprintf("_%d", incrementNumber)
			log.Debugln("Truncated networkID exists, trying a new suffix:", socketName)
			incrementNumber++
		}
		log.Infoln("Creating new vde_switch with socket path:", socketName)
		// Force create_sockets to true
		createSockets = "true"
		managementSocketName = socketName + ".mgmt.sock"
	} else if socketName != "" && createSockets == "" {
		// Check the existing socket is a directory with a ctl socket in it
		if !fsutil.PathIsDir(socketName) {
			log.Errorln("Existing socket directory for network switch does not exist:", socketName)
			return errors.New("Supplied existing socket directory does not exist")
		}
		// Check there's a ctl socket in it
		if !fsutil.PathIsSocket(filepath.Join(socketName, "ctl")) {
			log.Errorln("Existing socket directory does not appear to be a vde_switch directory", socketName)
			return errors.New("Existing socket directory does not appear to be a vde_switch directory")
		}
		log.Infoln("Using existing socket for network:", socketName)
		// Throw a warning if the management socket doesn't exist
		if !fsutil.PathIsSocket(managementSocketName) {
			log.Warnln("Specified management socket doesn't exist! Some functions will not work.")
		}
	} else {
		// Generate a management socket name if one wasn't specified
		if managementSocketName == "" {
			managementSocketName = socketName + ".mgmt.sock"
		}
		log.Infoln("Creating new vde_switch with given socket path:", socketName)
	}

	var cmd *exec.Cmd
	var mgmtPipe io.WriteCloser
	if createSockets != "" {
		var err error
		// Start the VDE switch for the new network
		cmdArgs := []string{
			"--sock", socketName,
			"--mgmt", managementSocketName}

		// If group specified, add it
		if socketGroup != "" {
			cmdArgs = append(cmdArgs, "--group", socketGroup)
		}

		cmd := fsutil.LoggedCommand("vde_switch", cmdArgs...)
		mgmtPipe, err = cmd.StdinPipe()
		if err != nil {
			return errors.New("Error setting up stdin pipe for vde_switch.")
		}
		if err := cmd.Start(); err != nil {
			return errors.New("Error starting vde_switch for network.")
		}
	}

	// Stash the network info
	network := VDENetworkDesc{
		sockDir:          socketName,
		mgmtSock:         managementSocketName,
		mgmtPipe:         mgmtPipe,
		switchp:          cmd,
		pool4:            pool4,
		pool6:            pool6,
		networkEndpoints: make(VDENetworkEndpoints),
	}

	// Add the network
	this.mtx.Lock()
	defer this.mtx.Unlock()
	this.networks[req.NetworkID] = &network
	log.With(NetworkOptionSwitchSocket, socketName).
		With(NetworkOptionSwitchManagementSocket, managementSocketName).
		Infoln("Created new network")
	return nil
}

func (this *VDENetworkDriver) DeleteNetwork(req *network.DeleteNetworkRequest) error {
	log.With("NetworkID", req.NetworkID).Infoln("DeleteNetwork request received")

	if !this.networkExists(req.NetworkID) {
		return errors.New("Network does not exist.")
	}

	this.mtx.Lock()
	defer this.mtx.Unlock()
	network, _ := this.networks[req.NetworkID]

	// Check that all endpoints have been removed
	if len(network.networkEndpoints) > 0 {
		return errors.New("Network still in-use!")
	}

	// Kill the vde_switch process if we're in control of it.
	if network.mgmtPipe != nil {
		network.mgmtPipe.Close()
	}

	if network.switchp != nil {
		network.switchp.Process.Kill()
		network.switchp.Wait()
		// Delete socket directories only if we controlled the process to start with
		os.RemoveAll(network.sockDir)
		os.Remove(network.mgmtSock)
	}
	delete(this.networks, req.NetworkID)

	return nil
}

func (this *VDENetworkDriver) CreateEndpoint(req *network.CreateEndpointRequest) (*network.CreateEndpointResponse, error) {
	log := log.With("EndpointID", req.EndpointID)

	if req.Interface != nil {
		log.With("NetworkID", req.NetworkID).
			With("Address", req.Interface.Address).
			With("AddressIPv6", req.Interface.AddressIPv6).
			With("MACAddress", req.Interface.MacAddress).
			Infoln("CreateEndpoint request received with parameters")
	} else {
		log.With("NetworkID", req.NetworkID).
			Infoln("CreateEndpoint request received")
	}

	if !this.networkExists(req.NetworkID) {
		return nil, errors.New("Network does not exist")
	}
	// Grab the network and hold onto it till we finish.
	// This is so no-one deletes it while we're setting up an endpoint.
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	vdeNetwork, _ := this.networks[req.NetworkID]
	// Check if the endpoint already exists
	if vdeNetwork.EndpointExists(req.EndpointID) {
		return nil, errors.New("Endpoint already exists")
	}

	// Start instantiating a new endpoint
	endpoint := &VDENetworkEndpoint{}

	if req.Interface.Address != "" {
		ip, net, err := net.ParseCIDR(req.Interface.Address)
		if err != nil {
			log.Errorln("Supplied IPv4 address is unparseable:", req.Interface.Address)
			return nil, errors.New("Unparseable IPv4 address supplied")
		}
		// Fill in the endpoint struct
		endpoint.address = ip
		endpoint.addressNet = *net
		log.Debugln("Endpoint IPv4 Address:", endpoint.GetIPv4CIDRAddress())
	}

	if req.Interface.AddressIPv6 != "" {
		ip, net, err := net.ParseCIDR(req.Interface.AddressIPv6)
		if err != nil {
			log.Errorln("Supplied IPv6 address is unparseable:", req.Interface.Address)
			return nil, errors.New("Unparseable IPv6 address supplied")
		}
		// Fill in the endpoint struct
		endpoint.address6 = ip
		endpoint.addressNet6 = *net
		log.Debugln("Endpoint IPv6 Address:", endpoint.GetIPv6CIDRAddress())
	}

	if req.Interface.MacAddress != "" {
		var err error
		endpoint.macAddress, err = net.ParseMAC(req.Interface.MacAddress)
		if err != nil {
			return nil, errors.New("Unparseable MAC address requested")
		}
	} else {
		endpoint.macAddress = randMACAddress()
		log.Debugln("Generated MAC Address:", endpoint.GetMACAddress())
	}

	// Figure out which gateway we want to use for the IPs we've picked
	endpoint.gateway = vdeNetwork.GetGateway(endpoint.address)
	endpoint.gateway6 = vdeNetwork.GetGateway(endpoint.address6)

	log.Debugln("Endpoint IPv4 Gateway:", endpoint.gateway.String())
	log.Debugln("Endpoint IPv6 Gateway:", endpoint.gateway.String())

	// Add the endpoint to the network
	vdeNetwork.mtx.Lock()
	defer vdeNetwork.mtx.Unlock()
	vdeNetwork.networkEndpoints[req.EndpointID] = endpoint

	// Construct a response
	resp := &network.CreateEndpointResponse{}
	if req.Interface == nil {
		resp.Interface = &network.EndpointInterface{}
		resp.Interface.Address = endpoint.GetIPv4CIDRAddress()
		resp.Interface.AddressIPv6 = endpoint.GetIPv6CIDRAddress()
		resp.Interface.MacAddress = endpoint.GetMACAddress()
	}

	return resp, nil
}

func (this *VDENetworkDriver) DeleteEndpoint(req *network.DeleteEndpointRequest) error {
	if !this.networkExists(req.NetworkID) {
		return errors.New("Network does not exist")
	}
	// Grab the network and hold onto it till we finish. This is so no-one deletes it while we're setting up an endpoint.
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	vdeNetwork, _ := this.networks[req.NetworkID]
	// Check the endpoint exists
	if !vdeNetwork.EndpointExists(req.EndpointID) {
		return errors.New("Endpoint does not exist")
	}
	// Grab the endpoint and hold onto it till we're done
	vdeNetwork.mtx.Lock()
	defer vdeNetwork.mtx.Unlock()
	vdeEndpoint, _ := vdeNetwork.networkEndpoints[req.EndpointID]

	// It's possible the endpoint is being killed while it's "Joined" - so ensure
	// we clean up it's processes.
	vdeEndpoint.KillTapCmd()
	vdeEndpoint.DeleteTapDevice()

	// Delete the endpoint
	delete(vdeNetwork.networkEndpoints, req.EndpointID)
	return nil
}

func (this *VDENetworkDriver) EndpointInfo(req *network.InfoRequest) (*network.InfoResponse, error) {
	if !this.networkExists(req.NetworkID) {
		return nil, errors.New("Network does not exist")
	}
	// Grab the network and hold onto it till we finish. This is so no-one deletes it while we're setting up an endpoint.
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	vdeNetwork, _ := this.networks[req.NetworkID]
	// Check the endpoint exists
	if !vdeNetwork.EndpointExists(req.EndpointID) {
		return nil, errors.New("Endpoint does not exist")
	}
	vdeNetwork.mtx.RLock()
	defer vdeNetwork.mtx.RUnlock()
	vdeEndpoint, _ := vdeNetwork.networkEndpoints[req.EndpointID]

	r := &network.InfoResponse{
		Value: make(map[string]string),
	}

	// Return information about the switch this is connected to
	r.Value["socket_dir"] = vdeNetwork.sockDir
	r.Value["management_socket"] = vdeNetwork.mgmtSock
	if vdeNetwork.switchp != nil {
		r.Value["switch_pid"] = string(vdeNetwork.switchp.Process.Pid)
		r.Value["create_sockets"] = ""
	} else {
		r.Value["switch_pid"] = ""
		r.Value["create_sockets"] = "true"
	}

	if vdeEndpoint.tapPlugCmd != nil {
		r.Value["plug_pid"] = string(vdeEndpoint.tapPlugCmd.Process.Pid)
	} else {
		r.Value["plug_pid"] = ""
	}

	r.Value["tap_device"] = vdeEndpoint.tapDevName

	return r, nil
}

func (this *VDENetworkDriver) Join(req *network.JoinRequest) (*network.JoinResponse, error) {
	log := log.With("EndpointID", req.EndpointID).With("SandboxKey", req.SandboxKey)
	log.Infoln("Join Request Received")

	if !this.networkExists(req.NetworkID) {
		return nil, errors.New("Network does not exist")
	}
	// Grab the network and hold onto it till we finish. This is so no-one deletes it while we're setting up an endpoint.
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	vdeNetwork, _ := this.networks[req.NetworkID]
	// Check the endpoint exists
	if !vdeNetwork.EndpointExists(req.EndpointID) {
		return nil, errors.New("Endpoint does not exist")
	}
	// Grab the endpoint and hold onto it till we're done
	vdeNetwork.mtx.Lock()
	defer vdeNetwork.mtx.Unlock()
	vdeEndpoint, _ := vdeNetwork.networkEndpoints[req.EndpointID]

	// It shouldn't really be possible to get here. For now fail, in future,
	// maybe blow away the old endpoint if it's hanging around?
	if vdeEndpoint.tapDevName == "" {
		vdeEndpoint.tapDevName = InterfacePrefix + req.EndpointID[:11]
	} else {
		log.Errorln("Tap device still exists for endpoint:", vdeEndpoint.tapDevName)
		return nil, errors.New("Tap device still exists for endpoint")
	}

	if err := fsutil.CheckExec("ip", "tuntap", "add", "dev", vdeEndpoint.tapDevName, "mode", "tap"); err != nil {
		return nil, errors.New("Error creating tap device")
	}

	failedDeviceSetup := new(bool)
	*failedDeviceSetup = true
	defer func() {
		if *failedDeviceSetup {
			if err := fsutil.CheckExec("ip", "link", "delete", "dev", vdeEndpoint.tapDevName); err != nil {
				log.Errorln("Error removing created tap device:", vdeEndpoint.tapDevName)
			}
		}
	}()

	if err := fsutil.CheckExec("ip", "link", "set", "dev", vdeEndpoint.tapDevName, "address", vdeEndpoint.macAddress.String()); err != nil {
		return nil, errors.New("Error setting MAC address")
	}

	if err := fsutil.CheckExec("ip", "link", "set", "dev", vdeEndpoint.tapDevName, "up"); err != nil {
		return nil, errors.New("Error setting device up")
	}

	if vdeEndpoint.GetIPv4CIDRAddress() != "" {
		if err := fsutil.CheckExec("ip", "address", "add", vdeEndpoint.GetIPv4CIDRAddress(), "dev", vdeEndpoint.tapDevName); err != nil {
			return nil, errors.New(fmt.Sprintln("Error setting IPv4 address:", vdeEndpoint.GetIPv4CIDRAddress()))
		}
	}

	if vdeEndpoint.GetIPv6CIDRAddress() != "" {
		if err := fsutil.CheckExec("ip", "address", "add", vdeEndpoint.GetIPv6CIDRAddress(), "dev", vdeEndpoint.tapDevName); err != nil {
			return nil, errors.New(fmt.Sprintln("Error setting IPv6 address:", vdeEndpoint.GetIPv6CIDRAddress()))
		}
	}

	// Plug the interface into the network switch
	cmd := fsutil.LoggedCommand("vde_plug2tap", "--sock", vdeNetwork.sockDir, vdeEndpoint.tapDevName)
	cmdPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.New("Failed to setup vde_plug2tap mgmt pipe")
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.New("Error starting vde_plug2tap for endpoint tap adaptor")
	}

	vdeEndpoint.tapPlugCmd = cmd
	vdeEndpoint.tapCmdPipe = cmdPipe

	// We have succeeded, do not delete the interface on function exit.
	*failedDeviceSetup = false

	r := &network.JoinResponse{
		InterfaceName: network.InterfaceName{
			SrcName:   vdeEndpoint.tapDevName,
			DstPrefix: InterfacePrefix,
		},
		Gateway:     vdeEndpoint.GetIPv4Gateway(),
		GatewayIPv6: vdeEndpoint.GetIPv6Gateway(),
	}

	return r, nil
}

func (this *VDENetworkDriver) Leave(req *network.LeaveRequest) error {
	if !this.networkExists(req.NetworkID) {
		return errors.New("Network does not exist")
	}
	// Grab the network and hold onto it till we finish. This is so no-one deletes it while we're setting up an endpoint.
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	vdeNetwork, _ := this.networks[req.NetworkID]
	// Check the endpoint exists
	if !vdeNetwork.EndpointExists(req.EndpointID) {
		return errors.New("Endpoint does not exist")
	}
	// Grab the endpoint and hold onto it till we're done
	vdeNetwork.mtx.Lock()
	defer vdeNetwork.mtx.Unlock()
	vdeEndpoint, _ := vdeNetwork.networkEndpoints[req.EndpointID]

	// Kill off the network connection processes
	vdeEndpoint.tapCmdPipe.Close()
	vdeEndpoint.tapPlugCmd.Process.Kill()
	vdeEndpoint.tapPlugCmd.Wait()

	// Strictly speaking we should remove the endpoint here. However, it's not
	// in the root namespace yet and we don't know where it is. As a kind of
	// hacky work-around, we rely on the fact that DeleteEndpoint will be called
	// right after this, and delete it there, when it should be back.

	return nil
}

func (this *VDENetworkDriver) DiscoverNew(req *network.DiscoveryNotification) error {
	log.Warnln("DiscoverNew: Received discovery notification:", req)
	return nil
}

func (this *VDENetworkDriver) DiscoverDelete(req *network.DiscoveryNotification) error {
	log.Warnln("DiscoverDelete: Received discovery delete:", req)
	return nil
}

func (this *VDENetworkDriver) ProgramExternalConnectivity(req *network.ProgramExternalConnectivityRequest) error {
	log.Warnln("ProgramExternalConnectivity: Unimplmented function called")
	return nil
}

func (this *VDENetworkDriver) RevokeExternalConnectivity(req *network.RevokeExternalConnectivityRequest) error {
	log.Warnln("RevokeExternalConnectivity: Unimplmented function called")
	return nil
}

func NewVDENetworkDriver(socketRoot string) *VDENetworkDriver {
	return &VDENetworkDriver{
		socketRoot: socketRoot,
		networks:   make(map[string]*VDENetworkDesc),
	}
}

// TODO: do some checks to make sure we properly clean up
func main() {
	dockerPluginPath := kingpin.Flag("docker-net-plugins", "Listen path for the plugin.").Default("unix:///run/docker/plugins/vde.sock").URL()
	socketRoot := kingpin.Flag("socket-root", "Path where networks and sockets should be created").Default("/run/docker-vde-plugin").String()
	loglevel := kingpin.Flag("log-level", "Logging Level").Default("info").String()
	logformat := kingpin.Flag("log-format", "If set use a syslog logger or JSON logging. Example: logger:syslog?appname=bob&local=7 or logger:stdout?json=true. Defaults to stderr.").Default("stderr").String()
	kingpin.Parse()

	// Check for the programs we need to actually work
	fsutil.MustLookupPaths(
		"ip",
		"vde_switch",
		"vde_plug2tap",
	)

	flag.Set("log.level", *loglevel)
	flag.Set("log.format", *logformat)

	if !fsutil.PathExists(*socketRoot) {
		err := os.MkdirAll(*socketRoot, os.FileMode(0777))
		if err != nil {
			log.Panicln("socket-root does not exist.")
		}
	} else if !fsutil.PathIsDir(*socketRoot) {
		log.Panicln("socket-root exists but is not a directory.")
	}

	log.Infoln("VDE default socket directories:", *socketRoot)
	log.Infoln("Docker Plugin Path:", *dockerPluginPath)

	driver := NewVDENetworkDriver(*socketRoot)
	handler := network.NewHandler(driver)

	handler.ServeUnix("root", PluginName)
}
