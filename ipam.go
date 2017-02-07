package main

import (
	"errors"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/wrouesnel/go.log"

	"net"
	"sync"
	"github.com/ziutek/utils/netaddr"
	"github.com/docker/go-plugins-helpers/ipam"

	"encoding/binary"
)

// Find the lastAddr in an IPv4 address
func lastAddr(n *net.IPNet) (net.IP, error) { // works when the n is a prefix, otherwise...
	if n.IP.To4() == nil {
		return net.IP{}, errors.New("does not support IPv6 addresses.")
	}
	ip := make(net.IP, len(n.IP.To4()))
	binary.BigEndian.PutUint32(ip, binary.BigEndian.Uint32(n.IP.To4())|^binary.BigEndian.Uint32(net.IP(n.Mask).To4()))
	return ip, nil
}

// Represents a golang formatted IPAM network pool
type IPAMNetworkPool struct {
	addressSpace string
	pool         net.IPNet
	// Subpool - holds the list of IP addresses the IPAM driver can assign
	// out of. Defaults to being the pool if not specified.
	subpool		 net.IPNet
	gateway      net.IP

	// Mapping of IPs which have been assigned out of this pool.
	assignedIPs	 map[string]net.IP
	// Holds IP addresses like .0 which *technically* can be used but are
	// not by convention. This is only set when this is used as an IPAM
	// construct.
	unusableIPs map[string]net.IP

	mtx sync.Mutex
}

// Returns a driver IPAMNetworkPool
func NewIPAMPool(inp *ipam.RequestPoolRequest) (*IPAMNetworkPool, error) {
	if inp.Pool == "" {
		return nil, errors.New("Must specify an IPAM address pool.")
	}
	_, poolNetwork, err := net.ParseCIDR(inp.Pool)
	if err != nil {
		log.Errorln("Supplied CIDR is unparseable:", inp.Pool)
		return nil, errors.New("Could not parse IPAM address pool")
	}

	if poolNetwork == nil {
		log.Errorln("Supplied CIDR did not include a network", inp.Pool)
		return nil, errors.New("Could not parse IPAM address pool")
	}

	// Parse subpool network
	var subpoolNetwork *net.IPNet
	if inp.SubPool != "" {
		_, subpoolNetwork, err = net.ParseCIDR(inp.SubPool)
		if err != nil {
			log.Errorln("Supplied CIDR is unparseable:", inp.Pool)
			return nil, errors.New("Could not parse IPAM address subpool")
		}
	} else {
		// No subpool means just assign from the poolNetwork
		subpoolNetwork = poolNetwork
	}

	// Build the list of valid but disallowed IPs in the pool network.
	unusableIPs := make(map[string]net.IP)

	// Block 0 from the range.
	disallowed := (*subpoolNetwork).IP.Mask((*subpoolNetwork).Mask)
	unusableIPs[disallowed.String()] = disallowed

	// And broadcast if IPv4
	if broadcast, err := lastAddr(subpoolNetwork) ; err == nil {
		unusableIPs[broadcast.String()] = broadcast
	}

	return &IPAMNetworkPool{
		addressSpace: inp.AddressSpace,
		pool:         *poolNetwork,
		gateway:      nil,
		assignedIPs:  make(map[string]net.IP),
		subpool:	  *subpoolNetwork,
		unusableIPs:  unusableIPs,
	}, nil
}

// Returns a driver IPAMNetworkPool
func NewIPAMNetworkPool(inp *network.IPAMData) (*IPAMNetworkPool, error) {
	_, poolNetwork, err := net.ParseCIDR(inp.Pool)
	if err != nil {
		log.Errorln("Supplied CIDR is unparseable:", inp.Pool)
		return nil, errors.New("Could not parse IPAM address pool")
	}

	if poolNetwork == nil {
		log.Errorln("Supplied CIDR did not include a network", inp.Pool)
		return nil, errors.New("Could not parse IPAM address pool")
	}

	ip, _, err := net.ParseCIDR(inp.Gateway)
	if err != nil {
		log.Errorln("Supplied Gateway was unparseable", inp.Gateway)
		return nil, errors.New("Could not parse IPAM gateway")
	}

	return &IPAMNetworkPool{
		addressSpace: inp.AddressSpace,
		pool:         *poolNetwork,
		gateway:      ip,
		assignedIPs:  make(map[string]net.IP),
		subpool:      *poolNetwork,
		unusableIPs:  make(map[string]net.IP),
	}, nil
}

// isUsable checks the IP is on the allowed list. This is used to rule out
// "strange" IP address like .0 from being allocated.
func (this *IPAMNetworkPool) isUsable(probe net.IP) bool {
	if _, found := this.unusableIPs[probe.String()]; found {
		return false
	}

	// Regular IP usability checks should reject gateway IPs
	if probe.Equal(this.gateway) {
		return false
	}

	return true
}

// isAssigned internal implementation - does not lock and so is used from
// within this struct only.
func (this *IPAMNetworkPool) isAssigned(probe net.IP) bool {
	_, found := this.assignedIPs[probe.String()]
	return found
}

func (this *IPAMNetworkPool) IsAssigned(probe net.IP) bool {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	return this.isAssigned(probe)
}

func (this *IPAMNetworkPool) GetGateway(ip net.IP) net.IP {
	this.mtx.Lock()
	defer this.mtx.Unlock()

	return this.gateway
}

func (this *IPAMNetworkPool) SetGateway(ip net.IP) error {
	this.mtx.Lock()
	defer this.mtx.Unlock()

	if this.pool.Contains(ip) == false {
		return errors.New("Requested Gateway is not in IP Pool range (it would not work)")
	}

	this.gateway = ip
	return nil
}

// Assigns an IP from the pool. If IP is not nil, then only attempts to assign
// the given IP. IPs will only be assigned out of the subpool.
func (this *IPAMNetworkPool) AssignIP(ip net.IP) net.IP {
	// Lock until we have made a decision
	this.mtx.Lock()
	defer this.mtx.Unlock()

	if ip != nil {
		// Is the IP the gateway? (i.e. container wanting to become the gateway)
		if this.gateway.Equal(ip) {
			// The gateway is already checked against the pool IP. So just check
			// someone else hasn't claimed it already.
			// Note: the usability list is not checked here, since that list
			// always marks gateway IPs as unusable.
			if this.isAssigned(ip) == false {
				this.assignedIPs[ip.String()] = ip
				return ip
			}
			return nil
		}

		// Not containable in this pool
		if this.subpool.Contains(ip) == false {
			return nil
		}

		if this.isAssigned(ip) {
			return nil
		} else {
			this.assignedIPs[ip.String()] = ip
			return ip
		}
	}

	// Nil IP - probe the map with incrementing IPs still we find one we can use
	for ip := this.subpool.IP.Mask(this.subpool.Mask); this.subpool.Contains(ip); ip = netaddr.IPAdd(ip, 1) {
		if (this.isAssigned(ip) == false) && this.isUsable(ip) {
			this.assignedIPs[ip.String()] = ip
			return ip
		}
	}

	// Couldn't find anything to assign.
	return nil
}

func (this *IPAMNetworkPool) FreeIP(ip net.IP) {
	this.mtx.Lock()
	defer this.mtx.Unlock()

	delete(this.assignedIPs, ip.String())
}