package main

import (
	"errors"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/wrouesnel/go.log"

	"net"
)

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
