*Pre-Release - this is not fully tested, though it is functional*

# VDE2 Network Plugin for Docker
This is a small network plugin intended to allow using VDE2 networks via Tap
devices in docker containers. It is convenient for lightweight, low-level
network simulations where you want to use DHCP and other protocols.

The essence of the plugin is that it connects a tap adaptor to a `vde_switch`
process. This process is normally started by the plugin when a new
`vde` network is created, but can also be an existing switch process
(by passing the `socket_dir` parameter as an option).

New containers connected to the network are spawned with `vde_plug2tap`
processes to link them to the switch process.

# Example Usage

## Getting Started
Install dependencies;
```bash
apt-get install -y vde2
```

Starting the process:
```bash
sudo docker-vde-plugin --log-level debug
```

Creating a new docker network:
```bash
docker network create --driver vde --ip-range 192.168.123.0/24 \
    --subnet 192.168.123.0/24 vdetest
```

Starting a container:
```bash
docker run -it --net=vdetest --ip=192.168.123.2 ubuntu:wily /bin/bash
```

Note: at the current time there is no support for dynamically assigning
IP addresses to containers.

## Using with KVM/QEMU
You need a version of KVM/QEMU with VDE2 support built in. Ubuntu does
not ship this by default, despite shipping `vde2` binaries. It is simple
enough to build from source.

Since in this mode we probably want the switch socket in a known location
we need to pass additional parameters to the network on creation:

```bash
docker network create --driver=vde \
    -o "socket_dir"="/home/will/mynetwork" \
    -o "create_sockets"="true" \
    -o "socket_group"="will"
    --ip-range 192.168.123.0/24 \
    --subnet 192.168.123.0/24 \
    vdekvm
```

By default the network driver avoids trying to create sockets in custom
locations - i.e. in case you mis-type while trying to use an existing
socket.

We can then start a container on the network as normal:
```bash
docker run -it --net=vdekvm --ip=192.168.123.2 ubuntu:wily /bin/bash
```

But we can also link a virtual machine into the same network easily
with a command line like the following:
```bash
qemu-system-x86_64 -enabel-kvm -m 512M \
    -netdev vde,id=vde0,sock=/home/will/mynetwork \
    -device e1000,netdev=vde0 -hda someDiskImage.qcow2
```

The virtual machine then boots, and can connect and talk to the
container network seamlessly over a simulated ethernet connection.

## Note on VDE socket paths
`vde_switch` and `vde_plug2tap` both send the absolute path of their socket
directories to allow them to communicate. This means that you should pass the
absolute path of the socket directory to any additional container or VDE service
you want to join to the network manually. It's not a problem for the plugin 
because that stays in the host namespace.

### Benefits
The benefits of this mode of operation is in testing disk-images in
virtual machines, without needing to launch many separate images for
network services which are presumed to "just exist" on the network, or
which might normally be docker containers themselves.
