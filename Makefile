IMAGE ?= registry.k8s.io/resource-claim-image-configurator
TAG ?= latest

.PHONY: docker-build
docker-build:
	docker build -t $(IMAGE):$(TAG) .
