IMAGE ?= csi-driver-syncthing:latest
PLATFORMS ?= linux/amd64,linux/arm64
HELM_RELEASE ?= csi-driver-syncthing
HELM_NAMESPACE ?= csi-syncthing
HELM_CHART ?= ./config

.PHONY: deps fmt test docker-build docker-push helm-upgrade-install

deps:
	go mod tidy

fmt:
	gofmt -w api/v1alpha1/*.go cmd/*/*.go internal/*/*.go

test:
	go test ./...

docker-build:
	docker buildx build --platform $(PLATFORMS) --load --target manager -t $(IMAGE) .

docker-push:
	docker buildx build --platform $(PLATFORMS) --push --target manager -t $(IMAGE) .

helm-upgrade-install:
	helm upgrade --install $(HELM_RELEASE) $(HELM_CHART) --namespace $(HELM_NAMESPACE) --create-namespace
