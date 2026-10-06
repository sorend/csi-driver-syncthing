#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-csi-sanity}"
IMAGE="csi-driver-syncthing:csi-sanity"

if ! kind get clusters | grep -Fxq "$CLUSTER_NAME"; then
  kind create cluster --name "$CLUSTER_NAME" --config "$ROOT_DIR/integration-tests/csi-sanity/kind.yaml" --wait 3m
fi

docker build --target manager -t "$IMAGE" "$ROOT_DIR"
docker build -f "$ROOT_DIR/integration-tests/csi-sanity/Dockerfile" -t csi-sanity:v5.6.0 "$ROOT_DIR/integration-tests/csi-sanity"
kind load docker-image "$IMAGE" --name "$CLUSTER_NAME"
kind load docker-image csi-sanity:v5.6.0 --name "$CLUSTER_NAME"
kubectl config use-context "kind-$CLUSTER_NAME"
kubectl apply -f "$ROOT_DIR/config/crds/"
helm upgrade --install csi-driver-syncthing "$ROOT_DIR/config" \
  --namespace csi-syncthing --create-namespace \
  --set image.repository=csi-driver-syncthing \
  --set image.tag=csi-sanity \
  --set image.pullPolicy=IfNotPresent \
  --set csiSanity.enabled=true \
  --set csiSanity.controllerSocketDir=/var/lib/kubelet/plugins/csi.syncthing.io-controller

kubectl rollout status daemonset/csi-driver-syncthing-node -n csi-syncthing --timeout=3m
kubectl rollout status deployment/csi-driver-syncthing-csi-controller -n csi-syncthing --timeout=3m
kubectl delete pod csi-sanity -n csi-syncthing --ignore-not-found
kubectl apply -f "$ROOT_DIR/integration-tests/csi-sanity/pod.yaml"
deadline=$((SECONDS + 900))
while (( SECONDS < deadline )); do
  phase="$(kubectl get pod csi-sanity -n csi-syncthing -o jsonpath='{.status.phase}')"
  if [[ "$phase" == Succeeded || "$phase" == Failed ]]; then
    break
  fi
  sleep 2
done
kubectl logs -n csi-syncthing pod/csi-sanity
if [[ "$phase" != Succeeded ]]; then
  echo "csi-sanity did not succeed (pod phase: ${phase:-unknown})" >&2
  exit 1
fi
