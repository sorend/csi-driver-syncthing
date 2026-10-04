# csi-driver-syncthing

`csi-driver-syncthing` provisions Kubernetes PVCs as node-local directories replicated asynchronously by one Syncthing instance per worker. It targets Linux clusters and `ReadWriteOnce` filesystem volumes. It does not provide shared-filesystem consistency: use single-writer workloads that tolerate asynchronous replication.

## Install

Build the image and make it available to all cluster nodes (or push it to a registry and update the image fields under `config/manager/manager.yaml`):

```sh
make docker-build
```

Install the operator, permissions, CSI node components, CRDs, and StorageClass:

```sh
kubectl apply -f config/namespace.yaml
kubectl apply -f config/crd/bases/
kubectl apply -f config/rbac/role.yaml
kubectl apply -f config/rbac/node-agent.yaml
kubectl apply -f config/manager/manager.yaml
```

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
