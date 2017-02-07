package main

import (
	"os/exec"

	"io"
	"net"
	"sync"
)

type VDENetworkDesc struct {
	sockDir  string
	mgmtSock string
	mgmtPipe io.WriteCloser
	// vde_switch process
	switchp *exec.Cmd
	// Channel for vde_switch Wait() call
	switchpCh <-chan error
	// IPAM data for this network
	pool4 []*IPAMNetworkPool
	pool6 []*IPAMNetworkPool
	// Currently executed vde_plug2tap processes
	networkEndpoints VDENetworkEndpoints
	// Mutex for networkEndpoints
	mtx sync.RWMutex
}

// Check that the container switchp process exists, and is attached to an
// executing process. For switches not under our control, this always returns
// true.
func (this *VDENetworkDesc) IsRunning() bool {
	if this.switchpCh != nil {
		select {
		case <- this.switchpCh:
			// Closed channel means the process exited
			return false
		default:
			// Running process means its fine
			return true
		}
	}
	return true
}

// For a given IP, find a suitable gateway IP in the current network. Return nil
// if nothing suitable is found.
func (this *VDENetworkDesc) GetGateway(ip net.IP) net.IP {
	var searchPool []*IPAMNetworkPool
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

func (this *VDENetworkDesc) EndpointExists(endpointId string) bool {
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	_, found := this.networkEndpoints[endpointId]
	return found
}
