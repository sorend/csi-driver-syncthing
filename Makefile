IMAGE ?= csi-driver-syncthing:latest
PLATFORMS ?= linux/amd64,linux/arm64
HELM_RELEASE ?= csi-driver-syncthing
HELM_NAMESPACE ?= csi-syncthing
HELM_CHART ?= ./config
KIND_CLUSTER_NAME ?= csi-sanity
KUBERNETES_STORAGE_E2E_CLUSTER_NAME ?= csi-storage-e2e
KUBERNETES_VERSION ?= v1.34.0
KIND_NODE_IMAGE ?= kindest/node:$(KUBERNETES_VERSION)
CSI_STORAGE_E2E_ARTIFACTS ?= artifacts/kubernetes-storage-e2e
CSI_STORAGE_E2E_FULL_ARTIFACTS ?= artifacts/kubernetes-storage-e2e-full

.PHONY: deps fmt test docker-build docker-push helm-upgrade-install csi-sanity csi-sanity-clean kubernetes-storage-e2e kubernetes-storage-e2e-focused kubernetes-storage-e2e-full kubernetes-storage-e2e-clean

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

csi-sanity:
	KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) bash ./integration-tests/csi-sanity/run.sh

csi-sanity-clean:
	kind delete cluster --name $(KIND_CLUSTER_NAME)

kubernetes-storage-e2e:
	KIND_CLUSTER_NAME=$(KUBERNETES_STORAGE_E2E_CLUSTER_NAME) KUBERNETES_VERSION=$(KUBERNETES_VERSION) KIND_NODE_IMAGE=$(KIND_NODE_IMAGE) CSI_STORAGE_E2E_ARTIFACTS=$(CSI_STORAGE_E2E_FULL_ARTIFACTS) KUBERNETES_STORAGE_E2E_SUITE=full bash ./integration-tests/kubernetes-storage-e2e/run.sh

kubernetes-storage-e2e-focused:
	KIND_CLUSTER_NAME=$(KUBERNETES_STORAGE_E2E_CLUSTER_NAME) KUBERNETES_VERSION=$(KUBERNETES_VERSION) KIND_NODE_IMAGE=$(KIND_NODE_IMAGE) CSI_STORAGE_E2E_ARTIFACTS=$(CSI_STORAGE_E2E_ARTIFACTS) bash ./integration-tests/kubernetes-storage-e2e/run.sh

kubernetes-storage-e2e-full:
	KIND_CLUSTER_NAME=$(KUBERNETES_STORAGE_E2E_CLUSTER_NAME) KUBERNETES_VERSION=$(KUBERNETES_VERSION) KIND_NODE_IMAGE=$(KIND_NODE_IMAGE) CSI_STORAGE_E2E_ARTIFACTS=$(CSI_STORAGE_E2E_FULL_ARTIFACTS) KUBERNETES_STORAGE_E2E_SUITE=full bash ./integration-tests/kubernetes-storage-e2e/run.sh

kubernetes-storage-e2e-clean:
	kind delete cluster --name $(KUBERNETES_STORAGE_E2E_CLUSTER_NAME)
