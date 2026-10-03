IMAGE ?= csi-driver-syncthing:latest

.PHONY: deps fmt test docker-build docker-build-node docker-build-agent

deps:
	go mod tidy

fmt:
	gofmt -w api/v1alpha1/*.go cmd/*/*.go internal/*/*.go

test:
	go test ./...

docker-build:
	docker build --target manager -t $(IMAGE) .

docker-build-node:
	docker build --target csi-node -t $(IMAGE)-node .

docker-build-agent:
	docker build --target node-agent -t $(IMAGE)-agent .
