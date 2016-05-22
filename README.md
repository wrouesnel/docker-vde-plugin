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

## Example Usage
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