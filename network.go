package main

import (
	"os/exec"

	"io"
	"net"
	"sync"

	"github.com/ziutek/utils/netaddr"
)

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
