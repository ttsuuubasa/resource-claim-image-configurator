IMAGE ?= registry.k8s.io/dra-driver-image-configurator
TAG ?= latest

.PHONY: docker-build
docker-build:
	docker build -t $(IMAGE):$(TAG) .
