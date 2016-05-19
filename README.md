# VDE2 Network Plugin for Docker
This is a small network plugin intended to allow using VDE2 networks via Tap
devices in docker containers. It is convenient for lightweight, low-level
network simulations where you want to use DHCP and other protocols.

The essence of the plugin is that it connects a tap adaptor to a VDE switch
running in the host's networking space.
