PROJECT ?= piraeus-ha-controller
REGISTRY ?= quay.io/piraeusdatastore
ARCH ?= amd64
SEMVER ?= 0.0.0+$(shell git rev-parse --short HEAD)
TAG ?= latest
NOCACHE ?= false

help:
	@echo "Useful targets: 'update', 'upload'"

all: update upload

.PHONY: update
update:
	docker build --build-arg=SEMVER=$(SEMVER) --build-arg=GOARCH=$(ARCH) --no-cache=$(NOCACHE) --pull=$(NOCACHE) -t $(PROJECT):$(TAG) .

.PHONY: upload
upload:
	docker tag $(PROJECT):$(TAG) $(REGISTRY)/$(PROJECT):$(TAG)
	docker push $(REGISTRY)/$(PROJECT):$(TAG)
