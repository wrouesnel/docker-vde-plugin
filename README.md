[![Build Status](https://travis-ci.org/wrouesnel/docker-vde-plugin.svg?branch=master)](https://travis-ci.org/wrouesnel/docker-vde-plugin)

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

## Network Options
These options can be passed to a network when it is created via
the command line or `docker-compose`.

* `socket_dir` : specify an existing vde_switch socket directory to
  associate with a network.
* `create_sockets` : when used with `socket_dir` forces the plugin to
  start the vde_switch process if it does not already exist. This is a
  handy way to daisy-chain networks out-of-band from docker's handling,
  or to create networks to use with KVM/Qemu and docker together.
* `management_socket` : specify the path to the management socket for
  an existing `vde_switch` process. Harmless to leave out because we
  don't currently use it for anything.
* `socket_group_` : specify the group own for the created socket. Useful
  when you need to use it with user-space processes without privileges.

## Running as a docker container
The plugin should be able to run as a docker container.

```bash
docker run --net=host --privileged \
    -v /run/docker/plugins:/run/docker/plugins \
    -v /run/docker-vde-plugin:/run/docker-vde-plugin \
    wrouesnel/docker-vde-plugin:latest
```

This mode of operation is not extensively tested yet.

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
qemu-system-x86_64 -enable-kvm -m 512M \
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

# Development Notes
Vendoring is managed with `govendor`. You can do a blind update of vendored
packages with `govendor fetch +vendor`.
