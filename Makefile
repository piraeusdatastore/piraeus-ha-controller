PROJECT ?= piraeus-ha-controller
REGISTRY ?= quay.io/piraeusdatastore
ARCH ?= amd64
VERSION ?= $(shell git describe --tags --match "v*.*" HEAD)
TAG ?= $(VERSION)
NOCACHE ?= false

help:
	@echo "Useful targets: 'update', 'upload'"

all: update upload

.PHONY: update
update:
	docker build --build-arg=VERSION=$(VERSION) --build-arg=GOARCH=$(ARCH) --no-cache=$(NOCACHE) --pull=$(NOCACHE) -t $(PROJECT):$(TAG) .

.PHONY: upload
upload:
	docker tag $(PROJECT):$(TAG) $(REGISTRY)/$(PROJECT):$(TAG)
	docker push $(REGISTRY)/$(PROJECT):$(TAG)
