package main

import (
	"crypto/rand"
	"net"
)

func randMACAddress() net.HardwareAddr {
	macAddr := make([]byte, 6)
	rand.Read(macAddr)
	return net.HardwareAddr(macAddr)
}