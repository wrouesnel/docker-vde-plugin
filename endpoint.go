package main

import (
	"os/exec"

	"github.com/wrouesnel/go.log"

	"fmt"
	"io"
	"net"

	"github.com/wrouesnel/docker-vde-plugin/fsutil"
)

type VDENetworkEndpoints map[string]*VDENetworkEndpoint

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
