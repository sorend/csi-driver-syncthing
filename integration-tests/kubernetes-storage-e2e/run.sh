#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-csi-storage-e2e}"
KUBERNETES_VERSION="${KUBERNETES_VERSION:-v1.34.0}"
KIND_NODE_IMAGE="${KIND_NODE_IMAGE:-kindest/node:${KUBERNETES_VERSION}}"
ARTIFACTS_DIR="${CSI_STORAGE_E2E_ARTIFACTS:-${ROOT_DIR}/artifacts/kubernetes-storage-e2e}"
SUITE="${KUBERNETES_STORAGE_E2E_SUITE:-focused}"
IMAGE="csi-driver-syncthing:kubernetes-storage-e2e"
DRIVER_DIR="${ROOT_DIR}/integration-tests/kubernetes-storage-e2e"
TMP_DIR=""

case "$SUITE" in
  focused)
    E2E_FOCUS='External.Storage.*csi.syncthing.io.*volumes should store data'
    ;;
  full)
    E2E_FOCUS='External.Storage.*csi.syncthing.io'
    ;;
  *)
    echo "Unsupported Kubernetes storage e2e suite: $SUITE (expected focused or full)" >&2
    exit 1
    ;;
esac

mkdir -p "$ARTIFACTS_DIR"

collect_diagnostics() {
  local status=$?
  if (( status != 0 )) && kind get clusters | grep -Fxq "$CLUSTER_NAME"; then
    echo "Kubernetes storage e2e failed; collecting cluster diagnostics" >&2
    kubectl --context "kind-${CLUSTER_NAME}" get nodes -o wide >"${ARTIFACTS_DIR}/nodes.txt" 2>&1 || true
    kubectl --context "kind-${CLUSTER_NAME}" get pods -A -o wide >"${ARTIFACTS_DIR}/pods.txt" 2>&1 || true
    kubectl --context "kind-${CLUSTER_NAME}" get events -A --sort-by=.metadata.creationTimestamp >"${ARTIFACTS_DIR}/events.txt" 2>&1 || true
    kubectl --context "kind-${CLUSTER_NAME}" describe pods -A >"${ARTIFACTS_DIR}/pod-descriptions.txt" 2>&1 || true
    kubectl --context "kind-${CLUSTER_NAME}" logs -n csi-syncthing -l app.kubernetes.io/name=csi-driver-syncthing-node --all-containers --prefix >"${ARTIFACTS_DIR}/node-logs.txt" 2>&1 || true
    kubectl --context "kind-${CLUSTER_NAME}" logs -n csi-syncthing -l app.kubernetes.io/name=csi-driver-syncthing-csi-controller --all-containers --prefix >"${ARTIFACTS_DIR}/controller-logs.txt" 2>&1 || true
    kind export logs --name "$CLUSTER_NAME" "${ARTIFACTS_DIR}/kind-logs" >/dev/null 2>&1 || true
  fi
  if [[ -n "$TMP_DIR" ]]; then
    rm -rf "$TMP_DIR"
  fi
  return "$status"
}
trap collect_diagnostics EXIT

for tool in docker kind kubectl helm curl tar; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "Required command not found: $tool" >&2
    exit 1
  fi
done

if ! kind get clusters | grep -Fxq "$CLUSTER_NAME"; then
  kind create cluster \
    --name "$CLUSTER_NAME" \
    --config "$DRIVER_DIR/kind.yaml" \
    --image "$KIND_NODE_IMAGE" \
    --wait 5m
fi
kubectl config use-context "kind-${CLUSTER_NAME}"

case "$(uname -m)" in
  x86_64)
    KUBERNETES_ARCH=amd64
    ;;
  aarch64|arm64)
    KUBERNETES_ARCH=arm64
    ;;
  *)
    echo "Unsupported architecture for Kubernetes e2e release: $(uname -m)" >&2
    exit 1
    ;;
esac

TMP_DIR="$(mktemp -d)"
curl --fail --location --retry 3 \
  "https://dl.k8s.io/${KUBERNETES_VERSION}/kubernetes-test-linux-${KUBERNETES_ARCH}.tar.gz" \
  --output "${TMP_DIR}/kubernetes-test.tar.gz"
tar -xzf "${TMP_DIR}/kubernetes-test.tar.gz" -C "$TMP_DIR" kubernetes/test/bin/e2e.test
E2E_TEST="${TMP_DIR}/kubernetes/test/bin/e2e.test"
chmod +x "$E2E_TEST"

docker build --target manager -t "$IMAGE" "$ROOT_DIR"
kind load docker-image "$IMAGE" --name "$CLUSTER_NAME"
helm upgrade --install csi-driver-syncthing "$ROOT_DIR/config" \
  --namespace csi-syncthing --create-namespace \
  --set image.repository=csi-driver-syncthing \
  --set image.tag=kubernetes-storage-e2e \
  --set image.pullPolicy=IfNotPresent
kubectl apply -f "$DRIVER_DIR/storageclass.yaml"
kubectl rollout status daemonset/csi-driver-syncthing-node -n csi-syncthing --timeout=5m
kubectl rollout status deployment/csi-driver-syncthing-csi-controller -n csi-syncthing --timeout=5m

"$E2E_TEST" \
  --provider=skeleton \
  --kubeconfig="${KUBECONFIG:-${HOME}/.kube/config}" \
  --ginkgo.focus="$E2E_FOCUS" \
  --ginkgo.skip='\[Feature:|\[Disruptive\]' \
  --disable-log-dump \
  --report-dir="$ARTIFACTS_DIR" \
  --report-prefix=csi-storage- \
  --report-complete-ginkgo=true \
  --storage.testdriver="$DRIVER_DIR/testdriver.yaml"
