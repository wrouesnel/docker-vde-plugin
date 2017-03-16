
GO_SRC := $(shell find -type f -name "*.go")

SRC_ROOT = github.com/wrouesnel/docker-vde-plugin
DST_ROOT := build
BINDATA := assets/bindata.go
PROGNAME := docker-vde-plugin
VERSION ?= git:$(shell git rev-parse HEAD)
TAG ?= latest
CONTAINER_NAME ?= wrouesnel/$(PROGNAME):$(TAG)
DIND_CONTAINER_NAME ?= wrouesnel/$(PROGNAME)-dind:$(TAG)
BUILD_CONTAINER ?= docker-vde-plugin_build

all: vet test $(DST_ROOT)/$(PROGNAME)

# Simple go build
$(DST_ROOT)/$(PROGNAME): $(BINDATA) $(GO_SRC)
	GOOS=linux go build -a \
	-ldflags "-extldflags '-static' -X main.Version=$(VERSION)" \
	-o $(DST_ROOT)/$(PROGNAME) .

# Take a go build and turn it into a minimal container.
docker: $(DST_ROOT)/$(PROGNAME)
	docker run --name $(BUILD_CONTAINER) ubuntu:wily /bin/bash -c "apt-get update && apt-get install -y iproute2"
	docker cp $(DST_ROOT)/$(PROGNAME) $(BUILD_CONTAINER):$(PROGNAME)
	docker commit -c "ENTRYPOINT [ \"$(PROGNAME)\" ]" $(BUILD_CONTAINER) $(CONTAINER_NAME)
	docker rm $(BUILD_CONTAINER)

# Take a go build and create a docker-in-docker container.
dind: $(DST_ROOT)/$(PROGNAME)
	cp -f $(PROGNAME) docker/
	docker build -t $(CONTAINER_NAME):dind-1.12.1-block-$(TAG) docker

vet:
	go vet .

# Build the integration test binary. Don't run it because we need sudo.
docker-vde-plugin.test: $(GO_SRC)
	go test -c -v -cover -covermode count -o test-docker-vde-plugin

test: test-docker-vde-plugin
	sudo $(shell pwd)/docker-vde-plugin.test -test.coverprofile=docker-vde-plugin.test.out

$(BINDATA): .build/go-bindata build/bin/vde_plug build/bin/vde_switch
	mkdir -p assets
	.build/go-bindata -pkg="assets" -o $(BINDATA) -prefix=build/bin build/bin

.build/go-bindata:
	go get -v github.com/jteeuwen/go-bindata
	go build -o .build/go-bindata github.com/jteeuwen/go-bindata/go-bindata

# Build a mostly static vdeplug4 - we need to investigate improving this.
build/bin/vde_plug: CLONE_DIR = 3rdparty/vdeplug4
build/bin/vde_plug:
	[ ! -d $(CLONE_DIR) ] \
	&& git clone https://github.com/rd235/vdeplug4.git $(CLONE_DIR) \
	|| git -C $(CLONE_DIR) checkout 1403a3f1a17bb6ab74489c09519c67d565d3dae4
	( \
	cd $(CLONE_DIR) && \
	autoreconf -if && \
	LDFLAGS="-static" ./configure --enable-shared=no --enable-static=yes \
	--prefix=/ \
	)
	make -C $(CLONE_DIR)
	make -C $(CLONE_DIR) install DESTDIR=$(shell pwd)/.build/vdeplug4
	mkdir -p build/bin
	cp -f $(shell pwd)/.build/vdeplug4/bin/vde_plug build/bin/vde_plug
		
# Build a mostly static vde_switch - we need to investigate improving this.
build/bin/vde_switch: CLONE_DIR = 3rdparty/vde-2
build/bin/vde_switch: 
	[ ! -d $(CLONE_DIR) ] \
	&& git clone https://github.com/virtualsquare/vde-2.git $(CLONE_DIR) \
	|| git -C $(CLONE_DIR) checkout 6736126558ee915459e0a03bdfb223f8454bda7a
	( \
	cd $(CLONE_DIR) && \
	autoreconf -if && \
	LDFLAGS="-static" ./configure --enable-shared=no --enable-static=yes \
	--disable-cryptcab --disable-vde_over_ns --disable-router --disable-vxlan \
	--disable-tuntap --disable-pcap --disable-kernel-switch --disable-python \
	--prefix=/ \
	)
	make -C $(CLONE_DIR)
	make -C $(CLONE_DIR) install DESTDIR=$(shell pwd)/.build/vde-2
	mkdir -p build/bin
	cp -f $(shell pwd)/.build/vde-2/bin/vde_switch build/bin/vde_switch

.PHONY: docker test vet 
