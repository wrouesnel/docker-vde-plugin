// ipam_driver simply wraps the vde_network driver in a struct to make it
// satisfy the IPAM interface.

package main

import (
	"errors"

	"github.com/wrouesnel/go.log"

	"github.com/docker/go-plugins-helpers/ipam"
	"net"
	"fmt"
	"encoding/hex"

	"github.com/satori/go.uuid"
	"github.com/docker/libnetwork/netlabel"
)

const (
	IPAMDefaultAddressSpaceLocal = "local"
	IPAMDefaultAddressSpaceGlobal = "global"
)

type IPAMDriver struct {
	*VDENetworkDriver
}

// GetCapabilities implements IPAMDriver.GetCapabilities by wrapping the
// response from the VDE Driver.
func (this *IPAMDriver) GetCapabilities() (*ipam.CapabilitiesResponse, error) {
	// Technically, VDE can be global, but we have no way to know that.
	return &ipam.CapabilitiesResponse{RequiresMACAddress: true}, nil
}

// GetDefaultAddressSpaces returns some constant entries, since at the moment
// this concept doesn't really matter to VDE (since we explicitely want to allow
// overlaps.
func (this *VDENetworkDriver) GetDefaultAddressSpaces() (*ipam.AddressSpacesResponse, error) {
	log.Infoln("Got GetDefaultAddressSpaces request")
	return &ipam.AddressSpacesResponse{
		LocalDefaultAddressSpace: IPAMDefaultAddressSpaceLocal,
		GlobalDefaultAddressSpace: IPAMDefaultAddressSpaceGlobal,
	}, nil
}

// newPoolID makes a new pool UUID.
func (this *VDENetworkDriver) newPoolID() string {
	return hex.EncodeToString(uuid.NewV4().Bytes())
}

func (this *VDENetworkDriver) RequestPool(req *ipam.RequestPoolRequest) (*ipam.RequestPoolResponse, error) {
	// TODO adapt to follow the logging in the network driver
	log.With("AddressSpace", req.AddressSpace).
		With("Pool", req.Pool).
		With("SubPool", req.SubPool).
		With("IPv6", req.V6).
		With("Options", req.Options).
		Infoln("RequestPool request received.")

	if req.Pool == "" {
		return nil, errors.New("A valid subnet must be specified for vde IPAM")
	}

	newPool, err := NewIPAMPool(req)
	if err != nil {
		return nil, err
	}

	this.ipamMtx.Lock()
	defer this.ipamMtx.Unlock()

	poolId := this.newPoolID()
	this.ipam[poolId] = newPool

	return &ipam.RequestPoolResponse{
		PoolID: poolId,
		Pool: newPool.pool.String(),
		Data: make(map[string]string),
	}, nil
}

func (this *VDENetworkDriver) ReleasePool(req *ipam.ReleasePoolRequest) error {
	log := log.With("PoolID", req.PoolID)
	log.Infoln("ReleasePool request received")

	this.mtx.Lock()
	defer this.mtx.Unlock()

	_, found := this.ipam[req.PoolID]
	if found {
		log.Infoln("Removed pool from driver IPAM")
		delete(this.ipam, req.PoolID)
	} else {
		log.Warnln("PoolID does not exist in IPAM")
	}

	return nil
}

func (this *VDENetworkDriver) RequestAddress(req *ipam.RequestAddressRequest) (*ipam.RequestAddressResponse, error) {
	// TODO adapt to follow the logging in the network driver
	log := log.With("PoolID", req.PoolID).
		With("Address", req.Address).
		With("Options", req.Options)
	log.Infoln("RequestAddress request received")

	// Scan the IPAM pool for an address.
	this.ipamMtx.RLock()
	defer this.ipamMtx.RUnlock()
	pool, found := this.ipam[req.PoolID]
	if !found {
		return nil, errors.New(fmt.Sprintf("PoolID %s does not exist.", req.PoolID))
	}

	var ip net.IP
	if req.Address != "" {
		ip = net.ParseIP(req.Address)
		if ip == nil {
			return nil, errors.New(fmt.Sprintf("malformed IP address: %s", req.Address))
		}
	}

	// Gateway network requests are treated differently, since Docker will make
	// them without assigning a container. In our case we want docker to set the
	// default gateway, but we also want to let users assign a container to
	// act as the default gateway. VDE has no concept of bridge networks, so
	// the contract enforced is requiring an explicit IP request.
	if val, found := req.Options["RequestAddressType"]; found {
		if val == netlabel.Gateway {
			log.Infoln("Gateway Network Request")
			if err := pool.SetGateway(ip); err != nil {
				return nil, errors.New(fmt.Sprintf("Could not set default gateway for PoolID %s", req.PoolID))
			}
			log.Infoln("Gateway IP Successfully Set")
			return &ipam.RequestAddressResponse{
				Address: (&net.IPNet{ IP: ip, Mask: pool.pool.Mask }).String(),
				Data: make(map[string]string),
			}, nil
		}
	}

	rip := pool.AssignIP(ip)

	if rip == nil {
		return nil, errors.New(fmt.Sprintf("Could not assign address to PoolID %s", req.PoolID))
	}

	log.With("AssignedAddress", rip.String()).Infoln("Assigned IP")

	return &ipam.RequestAddressResponse{
		Address: (&net.IPNet{ IP: rip, Mask: pool.pool.Mask}).String(),
		Data: make(map[string]string),
	}, nil
}

func (this *VDENetworkDriver) ReleaseAddress(req *ipam.ReleaseAddressRequest) error {
	// TODO adapt to follow the logging in the network driver
	log.With("PoolID", req.PoolID).
		With("Address", req.Address).
		Infoln("ReleaseAddress request received")

	ip := net.ParseIP(req.Address)
	if ip == nil {
		return errors.New(fmt.Sprintf("malformed IP address: %s", req.Address))
	}

	this.ipamMtx.RLock()
	defer this.ipamMtx.RUnlock()

	pool, found :=  this.ipam[req.PoolID]
	if !found {
		return errors.New(fmt.Sprintf("PoolID %s not found.", req.PoolID))
	}

	pool.FreeIP(ip)

	return nil
}