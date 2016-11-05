# Dockerfile to setup a Docker-in-Docker image which includes the VDE network
# plugin. Inherits from ubuntu rather then alpine as the official image does.

FROM ubuntu:xenial

MAINTAINER Will Rouesnel <w.rouesnel@gmail.com>

RUN apt-key adv --keyserver hkp://p80.pool.sks-keyservers.net:80 \
    --recv-keys 58118E89F3A912897C070ADBF76221572C52609D
    
RUN echo "deb http://apt.dockerproject.org/repo ubuntu-xenial main" > /etc/apt/sources.list.d/docker.list

RUN apt-get update && apt-get install -y \
        docker-engine=1.12.1-0~xenial \
        iproute2 \
        vde2 \
        xfsprogs e2fsprogs
        
COPY docker-vde-plugin /usr/local/sbin/docker-vde-plugin

# Setup helper symlinks in the image
RUN ln -sf /var/run / && \
    ln -sf /var/tmp /

ENV BLOCK_DEVICE_SIZE=100G
ENV BLOCK_DEVICE_FS=xfs
ENV STORAGE_DRIVER=overlay2

ENTRYPOINT [ "/entrypoint.sh" ]
