
GO_SRC := $(shell find -type f -name "*.go")

SRC_ROOT = github.com/wrouesnel/docker-vde-plugin
PROGNAME := docker-vde-plugin
VERSION ?= git:$(shell git rev-parse HEAD)
TAG ?= latest
CONTAINER_NAME ?= wrouesnel/$(PROGNAME):$(TAG)
BUILD_CONTAINER ?= $(PROGNAME)_build

all: vet test $(PROGNAME)

# Simple go build
$(PROGNAME): $(GO_SRC)
	GOOS=linux go build -a \
	-ldflags "-extldflags '-static' -X main.Version=$(VERSION)" \
	-o $(PROGNAME) .

# Take a go build and turn it into a minimal container.
docker: $(PROGNAME)
	docker run --name $(BUILD_CONTAINER) ubuntu:wily "apt-get update && apt-get install -y vde2"
	docker cp $(PROGNAME) $(BUILD_CONTAINER):/$(PROGNAME)
	docker commit -c "ENTRYPOINT [ \"$(PROGNAME)\" ]" $(BUILD_CONTAINER) $(CONTAINER_NAME)
	docker rm $(BUILD_CONTAINER)

vet:
	go vet .

test:
	go test -v .

.PHONY: docker test vet
