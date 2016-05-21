package main

import (
	"crypto/rand"
	"net"
)

// Copied from include/linux/etherdevice.h
// This is the kernel's method of making random mac addresses
func randMACAddress() net.HardwareAddr {
	macAddr := make([]byte, 6)
	rand.Read(macAddr)
	macAddr[0] &= 0xfe; // clear multicast bit
	macAddr[0] |= 0x02 // set local assignment bit (IEEE802)
	return net.HardwareAddr(macAddr)
}