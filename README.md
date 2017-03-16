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
apt-get install -y iproute2
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

Note: docker-vde-plugin tries to be as low setup cost as possible. To this end
statically compiled binaries of `vde_switch` and `vde_plug` are included into
the binary as part of the build process and extracted on each execution. If you
experience problems, you may wish to install your distributions `vde2` package
(these binaries are not entirely static yet but should be broadly compatible).

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
* `num_switchports` : specify the number of ports to create on the switch
  (i.e. number of containers which can be attached). Default is 32.

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

## The vde IPAM driver
This plugin implements its own IPAM driver. The main feature is that clashing
subnets are *allowed* by the driver, since the network built by 
docker-vde-plugin is a layer 2 network. If you explicitely need to do IPAM,
you should use the docker default IPAM driver to enforce unique subnets.

### Default Gateways
Because `docker-vde-plugin` has no concept of the normal bridge-style default
gateways, they are handled quite differently. The IPAM driver will accept any
default gateway IP assignment, ignoring the subpool in favor of the IP pool.

The address is also not marked as in-use unless a container requests it
explicitely out of the subpool, but it will also never be assigned 
automatically.

This allows launching containers on the VDE network intended to act as the
default gateway between VDE and the host or other networking technologies.

#### Gateway Rules Summary
* Gateway IP can be assigned manually, but is never assigned automatically.
* A container with the gateway IP must have another network to provide its
  default route.
  * Use `docker-compose` or
  * Manually connect the container to the vde network after it is setup.

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

## Documentation References
* ipam: https://github.com/docker/libnetwork/blob/master/docs/ipam.md
* network: https://github.com/docker/libnetwork/blob/master/docs/design.md
