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
)

const (
	IF_PREFIX = "vdedocker"
)

type VDENetworkEndpoint struct {
	// vde_plug2tap cmd. nil if no container has actually attached yet.
	tapPlugCmd  *exec.Cmd
	address     net.IPAddr
	addressIPv6 net.IPAddr
	macAddress  net.HardwareAddr
}

type VDENetworkEndpoints map[string]*VDENetworkEndpoint

type VDENetworkDesc struct {
	sockDir  string
	mgmtSock string
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

func (this *VDENetworkDriver) getNetworkSocketDirName(networkId string) string {
	ctlDirName := filepath.Join(this.socketRoot, networkId)
	return ctlDirName
}

func (this *VDENetworkDriver) getNetworkManagementSocketName(networkId string) string {
	manageSocketName := filepath.Join(this.socketRoot, networkId+".management.sock")
	return manageSocketName
}

func (this *VDENetworkDriver) networkExists(networkId string) bool {
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	_, found := this.networks[networkId]
	return found
}

func (this *VDENetworkDriver) GetCapabilities() (network.CapabilitiesResponse, error) {
	// Technically, VDE can be global, but we have no way to know that.
	return network.CapabilitiesResponse{Scope: network.LocalScope}, nil
}

func (this *VDENetworkDriver) CreateNetwork(req *network.CreateNetworkRequest) error {
	// Check our state at a high level to see if we can make a new network.
	if this.networkExists(req.NetworkID) {
		return errors.New("Network already exists.")
	}
	// Check the socket network dir can be made
	netDir := this.getNetworkSocketDirName(req.NetworkID)
	if fsutil.PathExists(netDir) && fsutil.PathIsDir(netDir) {
		return errors.New("Network socket directory already exists.")
	} else if !fsutil.PathIsDir(netDir) {
		return errors.New("Network does not exist, but path exists and is not a directory.")
	}
	// Check the management socket can be made
	mgmtSock := this.getNetworkManagementSocketName(req.NetworkID)
	if fsutil.PathExists(mgmtSock) {
		return errors.New("Network management socket path already exists!")
	}
	// Make the network socket directory
	if err := os.Mkdir(netDir, os.FileMode(0777)); err != nil {
		return errors.New("Failed to create socket directory:" + err.Error())
	}
	// Start the VDE switch for the new network
	cmd := exec.Command("vde_switch", "--sock", netDir, "--mgmt", mgmtSock)
	if err := cmd.Start(); err != nil {
		return errors.New("Error starting vde_switch for network.")
	}
	// Stash the network info
	network := VDENetworkDesc{
		sockDir:          netDir,
		mgmtSock:         mgmtSock,
		switchp:          cmd,
		createData:       req,
		networkEndpoints: make(VDENetworkEndpoints),
	}

	// Add the network
	this.mtx.Lock()
	defer this.mtx.Unlock()
	this.networks[req.NetworkID] = &network
	log.Infoln("Created new network:", req.NetworkID, netDir, mgmtSock)
	return nil
}

func (this *VDENetworkDriver) DeleteNetwork(req *network.DeleteNetworkRequest) error {
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
	network.switchp.Process.Kill()
	// Delete socket directories
	os.RemoveAll(network.sockDir)
	os.Remove(network.mgmtSock)

	delete(this.networks, req.NetworkID)

	return nil
}

func (this *VDENetworkDriver) CreateEndpoint(req *network.CreateEndpointRequest) (*network.CreateEndpointResponse, error) {
	if !this.networkExists(req.NetworkID) {
		return errors.New("Network does not exist")
	}
	// Grab the network and hold onto it till we finish. This is so no-one deletes it while we're setting up an endpoint.
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	vdeNetwork, _ := this.networks[req.NetworkID]
	// Check if the endpoint already exists
	if vdeNetwork.EndpointExists(req.EndpointID) {
		return nil, errors.New("Endpoint already exists")
	}

	var v4Addr net.IP
	var v6Addr net.IP
	var macAddress net.HardwareAddr

	if req.Interface.Address != "" {
		v4Addr = net.ParseIP(req.Interface.Address)
	}

	if req.Interface.AddressIPv6 != "" {
		v6Addr = net.ParseIP(req.Interface.AddressIPv6)
	}

	if req.Interface.MacAddress != "" {
		var err error
		macAddress, err = net.ParseMAC(req.Interface.MacAddress)
		if err != nil {
			return nil, errors.New("Unparseable MAC address requested")
		}
	} else {
		macAddress = randMACAddress()
		log.Debugln("Generated MAC Address:", macAddress.String())
	}

	endpoint := &VDENetworkEndpoint{
		tapPlugCmd:  nil,
		address:     v4Addr,
		addressIPv6: v6Addr,
		macAddress:  macAddress,
	}

	vdeNetwork.mtx.Lock()
	defer vdeNetwork.mtx.Unlock()
	vdeNetwork.networkEndpoints[req.EndpointID] = endpoint

	return &network.CreateEndpointResponse{
		Interface: &network.EndpointInterface{
			MacAddress: macAddress.String(),
		},
	}
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
		return nil, errors.New("Endpoint does not exist")
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
	endpoint, _ := this.networks[req.EndpointID]

	r := &network.InfoResponse{
		Value: make(map[string]string),
	}

	// Return information about the switch this is connected to
	r.Value["SwitchSocketDirectory"] = vdeNetwork.sockDir
	r.Value["SwitchManagementSocket"] = vdeNetwork.mgmtSock
	r.Value["SwitchPID"] = vdeNetwork.switchp.Process.Pid

	return r, nil
}

func (this *VDENetworkDriver) Join(req *network.JoinRequest) (*network.JoinResponse, error) {
	if !this.networkExists(req.NetworkID) {
		return errors.New("Network does not exist")
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

	tapDevName := IF_PREFIX + req.EndpointID

	if err := fsutil.CheckExec("ip", "tuntap", "add", "dev", tapDevName, "mode", "tap"); err != nil {
		return nil, errors.New("Error creating tap device")
	}

	fsutil.CheckExec("ip", "link", "set", "dev")
}

func (this *VDENetworkDriver) Leave(req *network.LeaveRequest) error {

}

func (this *VDENetworkDriver) DiscoverNew(req *network.DiscoveryNotification) error {
	log.Debugln("Received discovery notification:", req)
	return nil
}

func (this *VDENetworkDriver) DiscoverDelete(req *network.DiscoveryNotification) error {
	log.Debugln("Received discovery delete:", req)
	return nil
}

func main() {
	dockerPluginPath := kingpin.Flag("docker-net-plugins", "Listen path for the plugin.").Default("unix:///run/docker/plugins/vde.sock").URL()
	socketRoot := kingpin.Flag("socket-root", "Path where networks and sockets should be created").Default("vde").String()
	kingpin.Parse()

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

	driver := VDENetworkDriver{}
	handler := network.NewHandler(driver)

	handler.ServeUnix("root", "vde2")
}
