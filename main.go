package docker_vde_plugin

import (
	"flag"
	"errors"
	"os/exec"
	"io/ioutil"

	. "github.com/docker/go-plugins-helpers/network"
	"github.com/wrouesnel/go.log"

)

// Implements network.Driver
type VDENetworkDriver struct {
	networkSwitches map[string]*exec.Cmd

	networkEndpoints map[string]*exec.Cmd
}

func (this *VDENetworkDriver) GetCapabilities() (CapabilitiesResponse, error) {
	// Technically, VDE can be global, but we have no way to know that.
	return CapabilitiesResponse{Scope:LocalScope}, nil
}

func (this *VDENetworkDriver) CreateNetwork(req *CreateNetworkRequest) error {
	ctlDir, err := ioutil.TempDir("", "docker_vde")
	if err != nil {
		return err
	}

	cmd := exec.Command("vde_switch", "--sock", ctlDir)
	if err := cmd.Start() err != nil {
		return err
	}

	log.Infoln("Created new network:", req.NetworkID)
	return nil
}

func (this *VDENetworkDriver) DeleteNetwork(req *DeleteNetworkRequest) error {
	cmd, ok := this.networkSwitches[req.NetworkID]
	if !ok {
		return errors.New("Requested deletion of non-existent network")
	}
	// No need to be gentle with vde_switch
	cmd.Process.Kill()
	return nil
}

func (this *VDENetworkDriver) CreateEndpoint(req *CreateEndpointRequest) (*CreateEndpointResponse, error) {

}

func (this *VDENetworkDriver) DeleteEndpoint(req *DeleteEndpointRequest) error {

}

func (this *VDENetworkDriver) EndpointInfo(req *InfoRequest) (*InfoResponse, error) {

}

func (this *VDENetworkDriver) Join(req *JoinRequest) (*JoinResponse, error) {

}

func (this *VDENetworkDriver) Leave(req *LeaveRequest) error {

}

func (this *VDENetworkDriver) DiscoverNew(req *DiscoveryNotification) error {

}

func (this *VDENetworkDriver) DiscoverDelete(req *DiscoveryNotification) error {

}

driver := VDENetworkDriver{}
handler := network.NewHandler(driver)

handler.ServeUnix("root", "vde2")