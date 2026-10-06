# csi-driver-syncthing

`csi-driver-syncthing` provisions Kubernetes PVCs as node-local directories replicated asynchronously by one Syncthing instance per worker. It targets Linux clusters and `ReadWriteOnce` filesystem volumes. It does not provide shared-filesystem consistency: use single-writer workloads that tolerate asynchronous replication.

## Install

Install the Helm chart from `config/`:

```sh
helm upgrade --install csi-driver-syncthing ./config \
  --namespace csi-syncthing \
  --create-namespace
```

The chart installs the controller, node components, CRDs, and `syncthing` StorageClass. The driver image defaults to `ghcr.io/sorend/csi-driver-syncthing:latest`; override it with `--set image.repository=... --set image.tag=...` if needed. Chart values are in `config/values.yaml`.

The worker nodes need persistent writable `/var/lib/csi-syncthing` storage, Linux mount propagation, and TCP/UDP port 22000 open between Syncthing peers. The node CSI plugin and mount setup helper require privileged access. The Syncthing GUI listens only on loopback; the node agent reads its API key from Syncthing's local config.

## Configure

The default `syncthing` StorageClass uses `WaitForFirstConsumer`, waits for initial synchronization, and requests a 24-hour replica retention period. Create a PVC:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: documents
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: syncthing
  resources:
    requests:
      storage: 10Gi
```

Optional StorageClass parameters are `initialSync` (`wait` or `none`) and `replicaRetention`. Requested capacity is advisory; the driver does not enforce disk quotas.

## Use

Mount the claim in a single-writer workload:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: documents
spec:
  containers:
    - name: app
      image: busybox:1.36
      command: ["sh", "-c", "echo hello > /data/hello.txt; sleep 3600"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: documents
```

Inspect provisioning, node identities, and replica state with:

```sh
kubectl get pvc,pv
kubectl get syncthingnodes
kubectl get syncthingvolumes
```

Syncthing maintains a separate local copy on each participating node. Changes replicate asynchronously; writes are not synchronously durable on other nodes. `ReadWriteMany`, block volumes, snapshots, expansion, and external backup integration are not supported.

## Example

See [`example/`](example/) for a cross-node PVC example that writes data on one node and reads the replicated data from another.

## Test with csi-sanity

Run the csi-sanity integration suite on a local Kind cluster (requires Docker, Kind, kubectl, and Helm):

```sh
make csi-sanity
```

Details and cleanup instructions are in [`integration-tests/csi-sanity/`](integration-tests/csi-sanity/). Use `make csi-sanity-clean` to delete the local cluster.

The separate **CSI sanity** GitHub Actions workflow runs this suite on pushes and pull requests.

## Test Kubernetes storage behavior

Run the upstream Kubernetes external CSI storage tests on a local three-node Kind cluster (requires Docker, Kind, kubectl, Helm, curl, and tar):

```sh
make kubernetes-storage-e2e
```

The workflow pins Kubernetes test and Kind node images to `v1.34.0`, installs the chart and a test StorageClass with `initialSync: none`, and runs the upstream `volumes should store data` test for `csi.syncthing.io`, skipping optional-feature and disruptive cases. Reports are written to `artifacts/kubernetes-storage-e2e/`. Use `make kubernetes-storage-e2e-clean` to delete the cluster. `KUBERNETES_VERSION`, `KIND_NODE_IMAGE`, and `KUBERNETES_STORAGE_E2E_CLUSTER_NAME` can be overridden for local runs.

The **Kubernetes storage e2e** GitHub Actions workflow runs this suite on pushes and pull requests and uploads reports and failure diagnostics.

The separate **Kubernetes storage e2e full suite** workflow selects all upstream external storage tests for this driver, excluding feature-tagged and disruptive tests. Run it locally with `make kubernetes-storage-e2e-full`; its reports are written to `artifacts/kubernetes-storage-e2e-full/`. The full-suite CI workflow also supports manual runs through GitHub Actions.
