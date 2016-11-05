#!/bin/sh
set -e

# Head-off future problems by making sure /run and /tmp and tmpfs.
mount -t tmpfs none /run || echo "tmpfs mount /run failed. Is container privileged?" >&2 && exit
mount -t tmpfs none /tmp || echo "tmpfs mount /tmp failed. Is container privileged?" >&2 && exit

# If we have a block device request, then create and format a block device.
dd if=/dev/zero of=/var-lib-docker.raw bs=1 seek=$BLOCK_DEVICE_SIZE count=0
if [ $? != 0 ] ; then
    echo "Failed to initialize block device. Exiting." >&2
    exit 1
fi

mkdir -p /var/lib/docker
mount -o loop -t $BLOCK_DEVICE_FS /var-lib-docker.raw /var/lib/docker || \
    echo "Could not loop mount /var/lib/docker. Is container privileged?" >&2 \
    exit 1

# Startup the vde-network-plugin
docker-vde-plugin &
if [ $? != 0 ] ; then
    echo "docker-vde-plugin failed to start. Exiting." >&2
    exit 1
fi

exec docker daemon \
		--host=unix:///var/run/docker.sock \
		--host=tcp://0.0.0.0:2375 \
		--storage-driver=$STORAGE_DRIVER \
		"$@"

