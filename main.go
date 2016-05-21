package main

import (
	"errors"
	"os/exec"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/wrouesnel/go.log"

	"github.com/wrouesnel/docker-vde-plugin/fsutil"
	"gopkg.in/alecthomas/kingpin.v2"
	"net"
	"os"
	"path/filepath"
	"sync"
	"fmt"
	"flag"
	"io"
	//"encoding/hex"
	//"encoding/base64"
)

const (
	IF_PREFIX = "vdep"
)

type VDENetworkEndpoint struct {
	// vde_plug2tap cmd. nil if no container has actually attached yet.
	tapPlugCmd  *exec.Cmd
	tapCmdPipe	io.WriteCloser
	// IPv4 address if assigned
	address     net.IP
	addressNet	net.IPNet
	// IPv6 address if assigned
	address6	net.IP
	addressNet6 net.IPNet
	// Hardware address (always assigned)
	macAddress  net.HardwareAddr
	// Current tap device. Empty means no tap currently instantiated.
	tapDevName  string

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

type VDENetworkDesc struct {
	sockDir  string
	mgmtSock string
	mgmtPipe io.WriteCloser
	// vde_switch process
	switchp *exec.Cmd
	// network creation data which made this network
	createData *network.CreateNetworkRequest
	// Currently executed vde_plug2tap processes
	networkEndpoints VDENetworkEndpoints
	// Mutex for networkEndpoints
	mtx sync.RWMutex
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

	// Check our state at a high level to see if we can make a new network.
	if this.networkExists(req.NetworkID) {
		return errors.New("Network already exists.")
	}
	// Check the socket network dir can be made
	// FIXME: handle name collisions
	var realnetDir string
	{
		incrementNumber := 0
		netDir := this.getNetworkSocketDirName(req.NetworkID)
		realnetDir = netDir
		for fsutil.PathExists(netDir) {
			realnetDir = netDir + fmt.Sprintf("_%d", incrementNumber)
			log.Debugln("Truncated networkID exists, trying a suffix:", netDir)
			incrementNumber++
		}
	}
	// Check the management socket can be made
	mgmtSock := realnetDir + ".mgmt.sock"

	// Start the VDE switch for the new network
	cmd := fsutil.LoggedCommand("vde_switch", "--sock", realnetDir, "--mgmt", mgmtSock)
	mgmtPipe, err := cmd.StdinPipe()
	if err != nil {
		return errors.New("Error setting up stdin pipe for vde_switch.")
	}

	if err := cmd.Start(); err != nil {
		return errors.New("Error starting vde_switch for network.")
	}
	// Stash the network info
	network := VDENetworkDesc{
		sockDir:          realnetDir,
		mgmtSock:         mgmtSock,
		mgmtPipe:		  mgmtPipe,
		switchp:          cmd,
		createData:       req,
		networkEndpoints: make(VDENetworkEndpoints),
	}

	// Add the network
	this.mtx.Lock()
	defer this.mtx.Unlock()
	this.networks[req.NetworkID] = &network
	log.Infoln("Created new network:", req.NetworkID,
		"Socket Directory:", realnetDir, "Management Socket:", mgmtSock)
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

	// Kill the vde_switch process
	network.mgmtPipe.Close()
	network.switchp.Process.Kill()
	network.switchp.Wait()
	// Delete socket directories
	os.RemoveAll(network.sockDir)
	os.Remove(network.mgmtSock)

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
	// Grab the network and hold onto it till we finish. This is so no-one deletes it while we're setting up an endpoint.
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
	// Kill the endpoint
	if vdeEndpoint.tapPlugCmd != nil {
		vdeEndpoint.tapPlugCmd.Process.Kill()
	}
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
	//endpoint, _ := this.networks[req.EndpointID]

	r := &network.InfoResponse{
		Value: make(map[string]string),
	}

	// Return information about the switch this is connected to
	r.Value["SwitchSocketDirectory"] = vdeNetwork.sockDir
	r.Value["SwitchManagementSocket"] = vdeNetwork.mgmtSock
	r.Value["SwitchPID"] = string(vdeNetwork.switchp.Process.Pid)

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

	// HOWTO: this gets tricky. We need to make a veth pair, then bridge the host side to a tap interface, which in turn
	// should be connected to the VDE socket. It's a lot of plate spinning, and I can't really see a way to get
	// DHCP to work out of this.
	// UPDATE: I'm less sure about this now - maybe we can get away with it because it does work with OpenVPN...

	// It shouldn't really be possible to get here. For now fail, in future,
	// maybe blow away the old endpoint if it's hanging around?
	if vdeEndpoint.tapDevName == "" {
		vdeEndpoint.tapDevName = IF_PREFIX + req.EndpointID[:11]
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

	if err := fsutil.CheckExec("ip", "link", "set", "dev", vdeEndpoint.tapDevName, "address", vdeEndpoint.macAddress.String() ); err != nil {
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
		InterfaceName: network.InterfaceName{vdeEndpoint.tapDevName,IF_PREFIX},
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

	if vdeEndpoint.tapDevName != "" {
		// Remove the interface
		if err := fsutil.CheckExec("ip", "link", "delete", "dev", vdeEndpoint.tapDevName); err != nil {
			log.Errorln("Error removing created tap device:", vdeEndpoint.tapDevName)
			return errors.New("Failed removing the created tap device")
		}
	} else {
		return errors.New("Endpoint has no assigned interface.")
	}

	// Tap device is removed
	vdeEndpoint.tapDevName = ""

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
		networks: make(map[string]*VDENetworkDesc),
	}
}

func main() {
	dockerPluginPath := kingpin.Flag("docker-net-plugins", "Listen path for the plugin.").Default("unix:///run/docker/plugins/vde.sock").URL()
	socketRoot := kingpin.Flag("socket-root", "Path where networks and sockets should be created").Default("vde").String()
	loglevel := kingpin.Flag("log-level", "Logging Level").Default("info").String()
	kingpin.Parse()

	flag.Set("log.level", *loglevel)

	if !fsutil.PathExists(*socketRoot) {
		err := os.MkdirAll(*socketRoot, os.FileMode(0777))
		if err != nil {
			log.Panicln("socket-root does not exist.")
		}
	} else if !fsutil.PathIsDir(*socketRoot) {
		log.Panicln("socket-root exists but is not a directory.")
	}

	log.Infoln("VDE Network Socket Directory:", *socketRoot)
	log.Infoln("Docker Plugin Path:", *dockerPluginPath)

	driver := NewVDENetworkDriver(*socketRoot)
	handler := network.NewHandler(driver)

	handler.ServeUnix("root", "vde2")
}
