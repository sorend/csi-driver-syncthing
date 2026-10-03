# csi-driver-syncthing — Detailed Design

**Status:** Draft design  
**Audience:** Kubernetes storage / CSI implementers  
**Primary goal:** Dynamically provision Kubernetes PersistentVolumes whose data is stored in node-local directories and asynchronously replicated by Syncthing to exactly the Kubernetes nodes that currently need the volume, with optional permanent off-cluster backup replicas.

---

## 1. Executive summary

`csi-driver-syncthing` is a Kubernetes CSI driver and companion controller that presents Syncthing-managed directories as Kubernetes PersistentVolumes.

The design deliberately treats Syncthing as a **replication engine**, not as a distributed filesystem. Each participating Kubernetes node maintains its own local filesystem replica of a PVC. Pods access that local replica via a normal bind mount. Syncthing asynchronously synchronizes that directory with the other replicas selected by the Kubernetes control plane.

The primary control-plane invariant is:

> **Kubernetes is the source of truth for which nodes should participate in a volume; Syncthing is responsible only for transferring and reconciling file contents among those nodes.**

One PersistentVolume maps to one Syncthing folder ID.

The driver initially targets `ReadWriteOnce` workloads. `ReadWriteMany` may be offered later only as an explicit eventual-consistency mode because Syncthing does not provide cross-node POSIX locking, cache coherency, atomic distributed writes, or immediate read-after-write consistency.

The system consists of:

- a CSI controller,
- the standard CSI sidecars,
- a CSI node plugin on every worker,
- a Syncthing instance on every worker,
- a small Syncthing node agent on every worker,
- one or more Kubernetes CRDs representing desired replication state,
- optionally, one or more persistent external Syncthing backup devices.

---

## 2. Goals

### 2.1 Primary goals

1. Dynamically provision PersistentVolumes through a normal Kubernetes `StorageClass`.
2. Store each volume in a node-local directory.
3. Run one persistent Syncthing identity per Kubernetes node.
4. Add a node to a volume's Syncthing folder only when Kubernetes schedules a workload that needs that volume there.
5. Remove or cache a replica when the node no longer needs it.
6. Wait for an initial replica to become usable before exposing it to the Pod.
7. Bind-mount the local Syncthing folder into the Pod through CSI.
8. Allow permanent off-cluster Syncthing replicas for disaster recovery.
9. Make reconciliation idempotent and controller-driven.
10. Avoid exposing the Syncthing management API outside the node.

### 2.2 Secondary goals

- replica retention/cache after unmount,
- storage capacity accounting,
- Prometheus metrics,
- multiple backup replicas,
- backup-side file versioning,
- encrypted untrusted backup targets,
- static PV import / disaster restore,
- topology-aware scheduling hints,
- explicit eventual-consistency RWX mode,
- VolumeSnapshot integration where meaningful.

### 2.3 Non-goals for the initial version

- synchronous replicated writes,
- block storage,
- distributed POSIX locking,
- transactional consistency across nodes,
- database-grade synchronous durability,
- transparent semantics equivalent to CephFS/NFS/Longhorn/Rook,
- automatic Syncthing introducer-based membership control,
- Windows support,
- arbitrary filesystem snapshots.

---

## 3. Storage semantics

The most important design constraint is that Syncthing is not a shared filesystem.

Each node sees a distinct local directory:

```text
worker-a                              worker-b

/var/lib/csi-syncthing/volumes/v1    /var/lib/csi-syncthing/volumes/v1
              │                                    │
              │          Syncthing                 │
              └──────── asynchronous sync ─────────┘
```

Applications therefore do **not** receive:

- cross-node file locking,
- coherent `mmap`,
- coherent page cache,
- distributed `flock`,
- synchronous visibility of writes,
- a global filesystem journal,
- cross-node atomic rename semantics,
- synchronous replication before `fsync()` returns.

### 3.1 Recommended initial access mode

The first implementation SHOULD support:

```text
ReadWriteOnce
```

The driver may technically maintain additional passive replicas, but Kubernetes should initially expose one active writer at a time.

A future `ReadWriteMany` mode should require explicit acknowledgement that it provides eventual synchronization and Syncthing conflict semantics rather than conventional shared-filesystem semantics.

### 3.2 Workloads

Reasonable candidates include:

- documents,
- media,
- uploaded assets,
- Git working data,
- large read-mostly datasets,
- user home-like files,
- build artifacts,
- application data with one active writer.

Poor candidates include:

- PostgreSQL,
- SQLite databases shared across nodes,
- etcd,
- Redis persistence with failover expectations,
- Lucene indexes with concurrent writers,
- workloads requiring distributed locks,
- workloads requiring synchronous replication guarantees.

---

## 4. High-level architecture

```text
                                      Kubernetes API
                                            │
                              ┌─────────────┴─────────────┐
                              │                            │
                    CSI external-provisioner       CSI external-attacher
                              │                            │
                              └─────────────┬──────────────┘
                                            │
                                ┌───────────▼───────────┐
                                │ Syncthing CSI         │
                                │ Controller            │
                                │                       │
                                │ Create/DeleteVolume   │
                                │ ControllerPublish     │
                                │ ControllerUnpublish   │
                                └───────────┬───────────┘
                                            │
                                     Kubernetes CRDs
                                            │
                  ┌─────────────────────────┼─────────────────────────┐
                  │                         │                         │
          ┌───────▼────────┐        ┌───────▼────────┐        ┌──────▼───────┐
          │ worker-a        │        │ worker-b        │        │ worker-c     │
          │                 │        │                 │        │              │
          │ CSI node plugin │        │ CSI node plugin │        │ CSI node     │
          │ node agent      │        │ node agent      │        │ node agent   │
          │ Syncthing       │◄──────►│ Syncthing       │        │ Syncthing    │
          └───────┬─────────┘        └───────┬─────────┘        └──────────────┘
                  │                          │
      /var/lib/.../volumes/v1    /var/lib/.../volumes/v1
                  │                          │
             bind mount                 bind mount
                  │                          │
                Pod A                      Pod B

                           worker replicas
                                  │
                                  ▼
                        external backup peer(s)
```

---

## 5. Components

## 5.1 CSI controller

The controller implements CSI controller RPCs.

Initial required RPCs:

- `CreateVolume`
- `DeleteVolume`
- `ControllerPublishVolume`
- `ControllerUnpublishVolume`
- `ValidateVolumeCapabilities`
- `ControllerGetCapabilities`
- `GetCapacity` if capacity-aware provisioning is implemented

Possible later RPCs:

- `ListVolumes`
- `ControllerExpandVolume`
- snapshot RPCs if a coherent snapshot model is implemented

The controller does **not** directly call node Syncthing APIs.

Instead, it updates Kubernetes desired state.

---

## 5.2 CSI node plugin

Runs as a privileged DaemonSet.

Responsibilities:

- `NodeGetInfo`
- `NodeGetCapabilities`
- `NodeStageVolume`
- `NodeUnstageVolume`
- `NodePublishVolume`
- `NodeUnpublishVolume`
- optionally `NodeGetVolumeStats`
- optionally `NodeExpandVolume`

It mounts only node-local paths.

Typical source:

```text
/var/lib/csi-syncthing/volumes/<volume-id>
```

Typical staging path:

```text
/var/lib/kubelet/plugins/kubernetes.io/csi/<driver>/...
```

`NodePublishVolume` performs a bind mount from the staging location into the kubelet-provided Pod target.

---

## 5.3 Syncthing node agent

The node agent is the only component allowed to manage the local Syncthing process.

Responsibilities:

1. discover the local Syncthing Device ID,
2. publish node identity/status to Kubernetes,
3. watch desired volume membership for its Kubernetes node,
4. create/delete local volume directories,
5. add/update peer device definitions,
6. create/update/delete Syncthing folders,
7. observe synchronization state,
8. publish replica status,
9. handle cache/retention policy,
10. avoid exposing the REST API remotely.

It communicates with Syncthing over loopback:

```text
http://127.0.0.1:8384
```

with the Syncthing REST API key.

---

## 5.4 Syncthing daemon

One Syncthing process runs per Kubernetes node.

The identity must survive Pod restarts and DaemonSet replacement.

Recommended host paths:

```text
/var/lib/csi-syncthing/
├── syncthing/
│   ├── config/
│   └── data/
└── volumes/
    ├── <volume-id>/
    └── <volume-id>/
```

Syncthing's own database, certificate, private key, and configuration MUST NOT reside inside a Syncthing-managed volume.

---

## 5.5 External backup peer

An optional non-Kubernetes Syncthing instance can permanently participate in all or selected volume folders.

Recommended configuration:

```text
folder type: receiveonly
file versioning: enabled
```

An untrusted backup target may instead use Syncthing's `receiveencrypted` capability if compatible with the required recovery design.

The external device should not determine Kubernetes replica membership.

---

## 6. Kubernetes resource model

A minimal design can use two CRDs.

## 6.1 `SyncthingNode`

Represents one Kubernetes worker's Syncthing identity.

Example:

```yaml
apiVersion: syncthing-storage.sorend.github.com/v1alpha1
kind: SyncthingNode
metadata:
  name: worker-a
spec:
  nodeName: worker-a
  deviceID: ABCDEFG-HIJKLMN-...
  addresses:
    - tcp://10.42.0.10:22000
status:
  ready: true
  syncthingVersion: "..."
  lastSeen: "..."
  conditions: []
```

The node agent owns most of this resource/status.

Potential fields:

```text
spec.nodeName
spec.deviceID
spec.addresses[]
spec.zone
spec.labels

status.ready
status.syncthingVersion
status.connectionStatus
status.lastSeen
status.conditions[]
```

---

## 6.2 `SyncthingVolume`

Represents one CSI volume / Syncthing folder.

Example:

```yaml
apiVersion: syncthing-storage.sorend.github.com/v1alpha1
kind: SyncthingVolume
metadata:
  name: st-pvc-21e9a
  finalizers:
    - syncthing-storage.sorend.github.com/volume-protection
spec:
  volumeHandle: st-pvc-21e9a
  folderID: st-pvc-21e9a

  desiredReplicas:
    - nodeName: worker-a
      mode: active

  backupReplicas:
    - deviceID: BACKUP-DEVICE-ID
      folderType: receiveonly

  initialSync:
    policy: wait

  retention:
    afterDetach: 24h

status:
  phase: Ready
  replicas:
    - nodeName: worker-a
      state: Ready
      completion: 100
      lastTransitionTime: "..."
  conditions: []
```

### Suggested replica states

```text
Pending
Configuring
Connecting
Syncing
Ready
Mounted
Cached
Removing
Error
```

### Suggested volume phases

```text
Creating
Available
Attaching
Ready
Deleting
Retained
Error
```

---

## 6.3 Optional `SyncthingReplica` CRD

A separate per-node resource can be introduced later if controller contention or status size becomes problematic.

Example:

```yaml
kind: SyncthingReplica
metadata:
  name: st-pvc-21e9a-worker-a
spec:
  volumeHandle: st-pvc-21e9a
  nodeName: worker-a
status:
  state: Ready
  completion: 100
```

For an MVP, embedding replica state in `SyncthingVolume` is simpler.

---

## 7. CSI driver registration

Driver name:

```text
csi.syncthing.io
```

Example `CSIDriver`:

```yaml
apiVersion: storage.k8s.io/v1
kind: CSIDriver
metadata:
  name: csi.syncthing.io
spec:
  attachRequired: true
  podInfoOnMount: false
  fsGroupPolicy: File
```

`attachRequired: true` is significant.

It causes Kubernetes' attach/detach machinery, via the CSI external-attacher, to invoke the controller publish lifecycle before kubelet mounting proceeds.

`ControllerPublishVolume` is therefore used as the authoritative signal:

> This volume is now required on this Kubernetes node.

This is preferable to independently watching Pods and inferring placement.

Reference:

- Kubernetes `CSIDriver` API: https://kubernetes.io/docs/reference/kubernetes-api/storage/csi-driver-v1/

---

## 8. StorageClass

Example:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: syncthing
provisioner: csi.syncthing.io

parameters:
  initialSync: "wait"
  replicaRetention: "24h"
  backupPolicy: "receiveonly"
  backupClass: "default"

reclaimPolicy: Retain
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: false
```

`WaitForFirstConsumer` is recommended because the first required worker is not known until the workload is scheduled.

Possible future parameters:

```text
replication.minPassiveReplicas
replication.maxPassiveReplicas
initialSync
replicaRetention
backupClass
backupRetention
backupFolderType
versioning
conflictPolicy
discovery
encryptedBackup
bandwidthClass
```

---

## 9. Volume identity

One CSI volume maps to one stable Syncthing folder ID.

Example:

```text
PVC UID:
  21e9a6c2-...

CSI volumeHandle:
  st-21e9a6c2-...

Syncthing folder ID:
  st-21e9a6c2-...
```

The folder label may carry human-readable metadata:

```text
namespace/name
```

but automation MUST use immutable IDs rather than labels.

Every worker uses a predictable local path:

```text
/var/lib/csi-syncthing/volumes/<volumeHandle>
```

The external backup may use a different physical path while retaining the same Syncthing folder ID.

---

## 10. CSI lifecycle

## 10.1 `CreateVolume`

Input:

```text
name
requested capacity
volume capabilities
StorageClass parameters
```

Actions:

1. validate capabilities,
2. reject unsupported access modes,
3. allocate a stable volume handle,
4. create `SyncthingVolume`,
5. configure permanent backup membership if requested,
6. return the CSI volume.

The call MUST be idempotent.

No Kubernetes worker replica is required yet.

Pseudo-flow:

```text
CreateVolume
     │
     ▼
validate request
     │
     ▼
allocate / resolve volumeHandle
     │
     ▼
create SyncthingVolume CR
     │
     ▼
ensure backup desired state
     │
     ▼
return CSI volume
```

---

## 10.2 `ControllerPublishVolume`

This is the critical membership operation.

Input:

```text
volumeHandle
nodeID
volumeCapability
```

Meaning:

> Ensure this node owns a usable local replica of this volume.

Actions:

1. map CSI `nodeID` to `SyncthingNode`,
2. add the node to `SyncthingVolume.spec.desiredReplicas`,
3. ensure existing participants are configured to share with it,
4. wait for the target node agent to configure the folder,
5. wait for the target node to reach the configured readiness threshold,
6. return success.

For `initialSync: wait`, readiness should require:

```text
local folder configured
AND relevant peers accepted folder
AND Syncthing folder healthy
AND needBytes == 0
AND needItems == 0
AND completion == 100
```

Care should be taken with the meaning of "100%" during peer disconnection. The implementation should combine multiple Syncthing status signals rather than trusting a single percentage blindly.

---

## 10.3 `NodeStageVolume`

Runs on the chosen worker.

Actions:

1. confirm the local node agent reports the replica Ready,
2. verify the local data directory exists,
3. create staging target,
4. bind-mount source directory to staging target if desired,
5. return success.

If the replica has not reached readiness, return a retryable gRPC error.

---

## 10.4 `NodePublishVolume`

Actions:

1. create Pod target path,
2. bind-mount the staged volume into the kubelet target,
3. optionally remount read-only if requested,
4. return success.

No network filesystem mount occurs.

---

## 10.5 `NodeUnpublishVolume`

Actions:

1. unmount the Pod bind mount,
2. return success.

This does not imply immediate Syncthing replica deletion because multiple Pods on the node may use the same volume.

---

## 10.6 `NodeUnstageVolume`

Actions:

1. verify no remaining node-local publish references,
2. unmount staging path,
3. optionally request/trigger a Syncthing scan,
4. allow final outgoing changes to become visible to peers,
5. return success.

The exact interaction between `NodeUnstageVolume` and controller detach should be tested carefully because kubelet/controller ordering may differ during failures.

---

## 10.7 `ControllerUnpublishVolume`

Meaning:

> Kubernetes no longer requires this volume to be attached to this node.

Actions:

1. mark replica as detached,
2. move it into `Cached` state if retention is enabled,
3. otherwise begin safe removal,
4. optionally wait until a durability peer has observed the latest state,
5. return success according to the selected detach policy.

A practical default should avoid blocking Pod lifecycle indefinitely on a disconnected backup peer.

Suggested policy choices:

```text
detachPolicy:
  local          # detach as soon as local unmount is safe
  anyPeer        # require one other healthy peer
  backup         # require designated backup peer
```

`local` is likely the safest MVP default operationally.

---

## 10.8 `DeleteVolume`

Actions:

1. set volume phase to `Deleting`,
2. prevent new attachments,
3. remove all Kubernetes worker memberships,
4. remove local cached replicas according to policy,
5. handle external backup according to PV reclaim policy / StorageClass policy,
6. remove Syncthing folder configuration,
7. remove finalizer,
8. delete CR.

For `Retain`, the external backup should remain intact and recovery metadata should be preserved.

For `Delete`, destruction of the final backup copy should be explicit and auditable.

---

## 11. Syncthing reconciliation

The node agent implements an idempotent reconciliation loop.

Conceptual algorithm:

```text
for each SyncthingVolume relevant to this node:

    if node should participate:
        ensure local volume directory exists

        ensure every required peer device exists in Syncthing config

        ensure folder exists with correct:
            folder ID
            local path
            folder type
            watcher settings
            peer membership

        inspect runtime state

        publish:
            Configuring
            Connecting
            Syncing
            Ready
            Error

    else if folder is still mounted:
        do nothing

    else if replica retention has not expired:
        keep folder as Cached

    else:
        remove folder from local Syncthing config
        remove local volume data if policy permits
```

The controller separately ensures the peer set converges consistently.

---

## 12. Syncthing REST API usage

Syncthing exposes a REST management API on its GUI port.

The node agent should use granular configuration endpoints rather than modifying `config.xml` directly.

Relevant APIs include:

```text
GET/POST        /rest/config/devices
GET/PUT/PATCH   /rest/config/devices/<id>
DELETE          /rest/config/devices/<id>

GET/POST        /rest/config/folders
GET/PUT/PATCH   /rest/config/folders/<id>
DELETE          /rest/config/folders/<id>

GET             /rest/config/restart-required

GET             /rest/db/completion?folder=<id>
GET             /rest/db/status?folder=<id>

GET             /rest/system/status
```

Syncthing documents that successful granular configuration changes are normally applied immediately, except for settings requiring restart. The agent should check `restart-required` when modifying process-level settings.

REST authentication should use an API key or Bearer token.

References:

- https://docs.syncthing.net/rest/config
- https://docs.syncthing.net/dev/rest.html
- https://docs.syncthing.net/rest/system-status-get
- https://docs.syncthing.net/v1.29.6/rest/db-completion-get.html
- https://docs.syncthing.net/v1.30.0/rest/db-status-get.html

---

## 13. Peer membership algorithm

Suppose:

```text
volume v1
current participants:
  worker-a
  backup-1

new participant:
  worker-b
```

The desired Syncthing folder peer set becomes:

```text
worker-a:
  v1 devices = worker-a, worker-b, backup-1

worker-b:
  v1 devices = worker-a, worker-b, backup-1

backup-1:
  v1 devices = worker-a, worker-b, backup-1
```

The implementation should converge rather than assume all writes succeed atomically.

For example:

```text
desired membership revision = 42

worker-a status: revision 42 configured
worker-b status: revision 42 configured
backup-1 status: revision 42 configured
```

A generation or hash of desired peer membership can simplify status reporting and race detection.

---

## 14. Node bootstrap

Each Syncthing node has a persistent Device ID.

Bootstrap:

```text
DaemonSet starts
      │
      ▼
Syncthing loads/generates persistent identity
      │
      ▼
node agent calls /rest/system/status
      │
      ▼
agent obtains Device ID
      │
      ▼
agent creates/updates SyncthingNode CR
      │
      ▼
controller may assign volumes
```

The identity storage must survive Kubernetes Pod recreation.

If a Kubernetes node is replaced but reuses the same node name with an empty host filesystem, it should be treated as a new Syncthing device unless identity restoration is intentionally implemented.

This must be detected rather than silently overwriting the old Device ID.

---

## 15. Peer addressing

For cluster-internal peers, prefer explicit deterministic addresses.

Example:

```text
tcp://10.42.0.10:22000
```

Potential address sources:

- Kubernetes Node InternalIP,
- dedicated host-network address,
- stable DNS mapping if available.

Initial implementation should consider disabling:

- global discovery,
- public relays,
- NAT traversal,

for Kubernetes workers.

External backup devices can use explicit reachable addresses.

The controller should not rely on Syncthing discovery as the authoritative inventory mechanism.

---

## 16. Network model

Syncthing control API:

```text
127.0.0.1:8384
```

Syncthing data replication:

```text
worker ↔ worker
worker ↔ backup
```

The Syncthing REST API should never need to be routable from application Pods.

NetworkPolicy/host firewall rules should allow only required peer synchronization traffic.

The exact Syncthing transport ports and protocol configuration should be explicitly configurable rather than hard-coded.

---

## 17. Initial synchronization gate

The node should not normally expose a newly-created replica before it is synchronized.

Example failure without a gate:

```text
worker-a has 500 GiB
Pod is rescheduled to worker-b
worker-b has downloaded only 2 GiB
Pod starts
Pod interprets missing files as real deletions/data absence
```

Preferred flow:

```text
ControllerPublish(worker-b)
        │
        ▼
configure folder
        │
        ▼
Connecting
        │
        ▼
Syncing: 12%
        │
        ▼
Syncing: 83%
        │
        ▼
Ready
        │
        ▼
ControllerPublish returns success
        │
        ▼
NodeStage / NodePublish
        │
        ▼
Pod starts
```

The node agent can observe:

```text
/rest/db/completion
/rest/db/status
```

Syncthing's `/rest/db/status` is documented as relatively expensive. It should be polled conservatively and cached.

---

## 18. Replica retention/cache

Deleting a node replica immediately after every detach wastes bandwidth.

Introduce:

```yaml
retention:
  afterDetach: 24h
```

State machine:

```text
Mounted
   │
   ▼
Detached
   │
   ▼
Cached
   │
   ├── reattached before expiry → Ready/Mounted
   │
   └── timeout
          │
          ▼
       Removing
          │
          ▼
       Absent
```

A cached copy may either:

1. remain a Syncthing folder and continue receiving updates, or
2. stop participating but keep files as a seed for later reuse.

Option 1 uses more synchronization bandwidth but maximizes fast reattachment.

Option 2 saves bandwidth but needs careful reintroduction/rescan behavior.

The MVP should keep cached replicas participating, because its behavior is simpler and safer.

---

## 19. Failure handling

## 19.1 Worker disappears abruptly

If a worker dies:

- Kubernetes may detach after timeout,
- the controller removes or deactivates desired membership,
- other replicas continue,
- no operation should require contacting the dead Syncthing REST API.

Data that existed only on the failed node and had not yet synchronized may be unavailable or lost.

This is an inherent asynchronous-replication property.

---

## 19.2 Backup peer unavailable

Policy must determine whether:

- attaching can proceed,
- detaching can proceed,
- volume is marked degraded.

Recommended behavior:

```text
attachment:
  proceed if a valid source peer exists

normal I/O:
  proceed locally

status:
  BackupDegraded=True

detach:
  do not block indefinitely by default
```

---

## 19.3 No source replica exists

If a volume is attached to a new worker and no existing participant contains the data:

- if the volume is known empty, initialize it,
- if a backup exists, restore from backup,
- otherwise return an explicit unavailable/data-source-lost condition.

Never silently treat an expected non-empty volume as empty.

---

## 19.4 Split-brain / multi-writer conflict

If two nodes independently modify the same file, Syncthing's normal conflict handling applies.

The driver must surface conflict health signals where possible.

For RWO, Kubernetes attachment semantics should minimize this scenario, but a partitioned old writer plus a newly started replacement can still create application-level split brain.

The driver cannot solve this completely because it does not participate synchronously in file writes.

---

## 19.5 Node identity replacement

If:

```text
nodeName = worker-a
old Device ID = AAA
new Device ID = BBB
```

the controller must detect it.

Suggested action:

1. set `SyncthingNode` condition `IdentityChanged`,
2. treat BBB as a new replica target,
3. preserve AAA until cleanup safety is established,
4. prevent ambiguous membership mutation.

---

## 20. Backup design

Replication alone is not a backup.

A deletion is also a valid synchronized filesystem change.

Therefore a backup peer should use at least one of:

- Syncthing file versioning,
- filesystem snapshots,
- object/tape backup beneath/after the replica,
- immutable backup storage.

Recommended external topology:

```text
Kubernetes workers
        │
        ▼
Syncthing backup peer
  folder type: receiveonly
        │
        ▼
filesystem snapshots / versioned backup
```

Syncthing folder types include `sendreceive`, `sendonly`, `receiveonly`, and `receiveencrypted`.

Reference:

- https://docs.syncthing.net/users/config.html

---

## 21. Disaster recovery

The external system should preserve both:

1. the Syncthing folder content,
2. a mapping of CSI volume identity to Syncthing folder identity.

Example retained metadata:

```yaml
volumeHandle: st-pvc-21e9a
folderID: st-pvc-21e9a
originalPVC:
  namespace: app
  name: uploads
capacity: 500Gi
createdAt: ...
```

After cluster loss:

1. build a new Kubernetes cluster,
2. deploy `csi-driver-syncthing`,
3. recreate/import PV metadata,
4. point the volume at the original folder ID,
5. schedule a Pod,
6. `ControllerPublishVolume` adds the new worker,
7. backup peer shares the folder to it,
8. new worker synchronizes,
9. node replica becomes Ready,
10. kubelet mounts the volume.

Static PV import should therefore be a first-class recovery path.

---

## 22. Reclaim policy

Suggested semantics:

### `Retain`

- remove Kubernetes worker replicas when no longer needed,
- retain the external backup folder,
- retain enough metadata for import/recovery.

### `Delete`

- remove worker replicas,
- request backup removal,
- wait for explicit reconciliation,
- delete retained metadata,
- complete only once configured destruction policy succeeds.

A finalizer should prevent accidental CR deletion before cleanup.

---

## 23. Security

### 23.1 REST API isolation

Bind Syncthing management to loopback:

```text
127.0.0.1:8384
```

Only the node agent receives the API key.

### 23.2 Kubernetes RBAC

The CSI controller needs only the resources required by CSI sidecars plus its CRDs.

The node agent should:

- read its own Node identity,
- read relevant `SyncthingVolume` resources,
- create/update its `SyncthingNode`,
- update only its own replica status if practical.

### 23.3 Privilege separation

The CSI node plugin needs mount privileges.

Syncthing itself does not necessarily need the same privilege set.

Where possible run them as separate containers/processes with separate security contexts.

### 23.4 Application isolation

Application Pods never receive:

- Syncthing API credentials,
- Syncthing configuration paths,
- Syncthing identity keys.

They see only the bind-mounted data directory.

---

## 24. Filesystem ownership and `fsGroup`

Kubernetes may request ownership modifications before presenting a volume.

The implementation must carefully choose `CSIDriver.spec.fsGroupPolicy`.

Potential issue:

- recursively changing ownership on a large Syncthing folder can generate large metadata activity,
- permission differences may synchronize depending on platform/settings.

This needs explicit integration tests.

For MVP, document the supported POSIX ownership model and avoid pretending arbitrary ownership semantics are free.

---

## 25. Capacity accounting

Syncthing does not provide storage isolation or reservation.

The real capacity is the node filesystem containing:

```text
/var/lib/csi-syncthing/volumes
```

The driver should eventually expose capacity based on host filesystem free space.

Problems to solve:

- a logical 500 GiB PVC is not a hard quota by default,
- multiple replicated PVCs consume physical space independently on every participating node,
- backup storage has separate capacity,
- scheduler placement should eventually consider destination free space.

Possible future approaches:

- filesystem project quotas,
- dedicated LVM/filesystem subvolumes,
- XFS project quotas,
- btrfs subvolumes,
- ZFS datasets.

An MVP may treat requested capacity as advisory but MUST document that clearly.

---

## 26. Expansion

Volume expansion can initially be unsupported:

```yaml
allowVolumeExpansion: false
```

If no hard quota exists, expansion may later become mostly metadata validation.

If quotas are implemented, `ControllerExpandVolume` / `NodeExpandVolume` must modify the underlying quota/dataset.

---

## 27. Snapshot semantics

A Syncthing folder version is not a crash-consistent multi-file snapshot.

Therefore CSI `VolumeSnapshot` should not initially be implemented merely by copying the current directory.

Possible future implementations:

- filesystem-native snapshots on the backup peer,
- LVM snapshots,
- ZFS/btrfs snapshots,
- application-assisted snapshots.

Snapshot support should promise only semantics the implementation can actually guarantee.

---

## 28. Observability

Expose Prometheus metrics.

Suggested controller metrics:

```text
syncthing_csi_volumes_total
syncthing_csi_attach_operations_total
syncthing_csi_attach_duration_seconds
syncthing_csi_detach_operations_total
syncthing_csi_reconcile_errors_total
```

Suggested replica metrics:

```text
syncthing_csi_replica_completion_percent
syncthing_csi_replica_need_bytes
syncthing_csi_replica_need_items
syncthing_csi_replica_state
syncthing_csi_replica_last_sync_timestamp
syncthing_csi_replica_cached
```

Suggested backup metrics:

```text
syncthing_csi_backup_connected
syncthing_csi_backup_completion_percent
syncthing_csi_backup_lag_bytes
```

Kubernetes Conditions should expose operator-visible health.

Example:

```yaml
conditions:
  - type: Ready
    status: "True"

  - type: BackupReady
    status: "False"
    reason: PeerDisconnected

  - type: ConflictDetected
    status: "False"
```

---

## 29. Events

Emit Kubernetes Events for major lifecycle changes:

```text
VolumeReplicaRequested
SyncthingFolderConfigured
ReplicaSyncStarted
ReplicaReady
ReplicaCached
ReplicaRemoved
BackupUnavailable
NodeIdentityChanged
ReplicaSyncFailed
ConflictDetected
VolumeRestoreStarted
VolumeRestoreCompleted
```

Do not emit events for every polling update.

---

## 30. Controller concurrency

Reconciliation must tolerate:

- duplicate CSI calls,
- controller restart,
- node-agent restart,
- Syncthing restart,
- stale status,
- simultaneous attach/detach,
- Kubernetes retry behavior.

All operations should be idempotent.

A useful rule:

> `spec` expresses desired topology; Syncthing config and `status` are eventually reconciled to it.

Avoid multi-step imperative state stored only in memory.

---

## 31. Configuration revision

A membership revision can reduce ambiguity.

Example:

```yaml
spec:
  membershipRevision: 17
  desiredReplicas:
    - worker-a
    - worker-b
```

Node status:

```yaml
status:
  replicas:
    worker-a:
      configuredRevision: 17
    worker-b:
      configuredRevision: 17
```

The actual revision may instead be a hash over normalized desired folder configuration.

---

## 32. Syncthing configuration details

For normal Kubernetes worker replicas:

```text
folder type: sendreceive
fsWatcherEnabled: true
```

For backup replicas:

```text
folder type: receiveonly
```

Potentially:

```text
folder type: receiveencrypted
```

for an untrusted off-site device.

The node agent should use Syncthing's configuration defaults endpoint or version-aware structs rather than assuming every config field forever remains identical.

Syncthing notes that its REST interface can change; therefore the project should explicitly support a bounded range of Syncthing versions and test those versions.

---

## 33. Introducers

Syncthing Introducers SHOULD NOT be the primary membership mechanism.

Reason:

- Kubernetes already knows the desired placement,
- operator reconciliation should remain authoritative,
- automatic transitive membership can fight desired-state cleanup,
- debugging is easier when one controller owns topology.

An introducer may eventually help with peer discovery, but folder membership should remain operator-controlled.

---

## 34. Scheduling and topology

A future scheduler-aware optimization can prefer nodes that already have a cached replica.

Potential design:

```text
Cached replica:
  worker-a 100%
  worker-b 100%

new Pod requires volume:
  prefer worker-a/worker-b
```

CSI topology or an auxiliary scheduler integration could expose this.

However, topology MUST remain an optimization, not the correctness mechanism.

A new node must always be able to synchronize from an existing source if capacity and connectivity permit.

---

## 35. StatefulSet considerations

RWO StatefulSets are a strong initial target.

Typical reschedule:

```text
Pod on worker-a
      │
      ▼
Pod terminates
      │
      ▼
worker-a remains a replica
      │
      ▼
Pod scheduled worker-b
      │
      ▼
ControllerPublish(worker-b)
      │
      ▼
worker-b synchronizes from worker-a and/or backup
      │
      ▼
Ready
      │
      ▼
Pod starts
```

This is particularly efficient if the previous worker remains healthy.

---

## 36. Durability model

The driver should document durability explicitly.

A successful local application write means:

> the data has been written according to the semantics of the local filesystem.

It does **not** mean:

> another Syncthing peer has synchronously committed the same write.

Consequently, abrupt destruction of a node can lose recently written but not yet synchronized changes.

The driver may report replication health, but cannot turn asynchronous Syncthing file replication into synchronous write acknowledgement without an additional I/O layer.

---

## 37. Suggested MVP behavior

Version 0.1 should deliberately remain small.

### Supported

- Linux
- Kubernetes CSI
- one Syncthing daemon per worker
- stable Syncthing identity per worker
- dynamic provisioning
- one folder per PV
- `ReadWriteOnce`
- `WaitForFirstConsumer`
- local bind mounts
- operator-controlled peer membership
- initial synchronization gate
- one optional external backup peer
- backup as `receiveonly`
- replica cache retention
- static PV import
- basic Prometheus metrics
- Kubernetes Conditions and Events

### Not supported

- RWX
- snapshots
- expansion
- hard storage quotas
- scheduler cache affinity
- encrypted backup
- multiple backup classes
- Windows
- cross-cluster active/active
- Syncthing introducer automation

---

## 38. Suggested implementation phases

### Phase 0 — prototype

Implement:

```text
Syncthing DaemonSet
node identity discovery
SyncthingNode CRD
SyncthingVolume CRD
manual volume CR creation
agent creates/removes Syncthing folders
```

Goal:

> Prove reliable reconciliation between Kubernetes desired state and Syncthing configuration.

### Phase 1 — CSI MVP

Add:

```text
CreateVolume
DeleteVolume
ControllerPublishVolume
ControllerUnpublishVolume
NodeStageVolume
NodeUnstageVolume
NodePublishVolume
NodeUnpublishVolume
```

Goal:

> A normal PVC can move between nodes safely enough for single-writer file workloads.

### Phase 2 — backup / restore

Add:

```text
external backup device
receive-only policy
versioning configuration
Retain behavior
static PV import
restore workflows
```

### Phase 3 — operations

Add:

```text
metrics
alerts
capacity monitoring
retention GC
upgrade tests
chaos tests
failure diagnostics
```

### Phase 4 — optimization

Evaluate:

```text
scheduler locality
multiple passive replicas
hard quotas
encrypted backup
RWX/eventual mode
filesystem snapshots
```

---

## 39. Testing plan

### 39.1 Unit tests

- CSI idempotency
- CRD reconciliation
- membership diffing
- identity change detection
- retention state machine
- reclaim policies
- API error handling

### 39.2 Integration tests

Use at least three worker containers/VMs plus one backup Syncthing device.

Test:

```text
create PVC
first mount
write files
unmount
mount different node
wait for synchronization
verify content
delete PVC
restore retained PVC
```

### 39.3 Failure tests

- kill Syncthing during sync,
- kill node agent during config update,
- restart CSI controller,
- disconnect backup,
- disconnect source worker,
- destroy worker before final sync,
- corrupt/change Syncthing Device ID,
- schedule destination with insufficient disk,
- induce simultaneous modifications,
- delete a large directory,
- repeatedly attach/detach,
- Kubernetes node force deletion.

### 39.4 Upgrade tests

Test a supported matrix of:

```text
Kubernetes versions
CSI sidecar versions
Syncthing versions
Linux distributions / kernels
```

---

## 40. Example complete attach flow

```text
User creates PVC
      │
      ▼
external-provisioner → CreateVolume
      │
      ▼
CSI controller creates SyncthingVolume
      │
      ▼
PVC/PV bound
      │
      ▼
scheduler chooses worker-b
      │
      ▼
external-attacher → ControllerPublishVolume(v1, worker-b)
      │
      ▼
controller adds worker-b to desiredReplicas
      │
      ├───────────── existing replicas reconcile peer membership
      │
      └───────────── worker-b agent:
                       mkdir local directory
                       add peer devices
                       add Syncthing folder
                       wait for peer connectivity
                       observe sync
                       status = Ready
      │
      ▼
ControllerPublishVolume succeeds
      │
      ▼
kubelet → NodeStageVolume
      │
      ▼
kubelet → NodePublishVolume
      │
      ▼
bind mount local folder into Pod
      │
      ▼
application performs ordinary local filesystem I/O
```

---

## 41. Example detach flow

```text
Pod terminates
      │
      ▼
NodeUnpublishVolume
      │
      ▼
NodeUnstageVolume
      │
      ▼
optional rescan / local readiness check
      │
      ▼
ControllerUnpublishVolume
      │
      ▼
replica state = Cached
      │
      ▼
retention timer expires
      │
      ▼
remove node from desired membership
      │
      ▼
peer configs converge
      │
      ▼
local Syncthing folder removed
      │
      ▼
local data deleted
```

---

## 42. Open design decisions

Before production implementation, explicitly decide:

1. What exact condition defines "initial sync complete"?
2. Should `ControllerPublishVolume` block for an arbitrarily large initial copy?
3. What timeout/retry semantics should be used for multi-terabyte volumes?
4. Does a cached replica remain actively synchronized?
5. What is required before detach: local-only, any peer, or backup confirmation?
6. How are node disk capacity and quotas enforced?
7. How is stale data recognized if all source peers disappear?
8. How is split-brain reported to the user?
9. How should Syncthing conflict files surface in Kubernetes health?
10. How are permissions, ownership, ACLs, xattrs, and symlinks supported?
11. Which Syncthing versions are supported?
12. Which Kubernetes/CSI sidecar versions are supported?
13. How is backup metadata preserved outside the destroyed cluster?
14. How are external backup device credentials/bootstrap handled?
15. What is the behavior if the external backup itself changes data?
16. Should persistent backup membership be modeled inside each volume or via a `BackupClass` CRD?
17. How does the driver prevent a node from starting a Pod from an incomplete or stale local cache?
18. What is the desired operator behavior when a folder is modified manually in the Syncthing GUI?

---

## 43. Future `BackupClass` concept

A useful later abstraction:

```yaml
apiVersion: syncthing-storage.sorend.github.com/v1alpha1
kind: SyncthingBackupClass
metadata:
  name: offsite
spec:
  devices:
    - deviceID: XXXXX-...
      addresses:
        - tcp://backup.example.net:22000

  folderType: receiveonly

  versioning:
    type: staggered

  retainOnVolumeDelete: true
```

StorageClass:

```yaml
parameters:
  backupClass: offsite
```

This keeps off-cluster device details out of every volume object.

---

## 44. Future RWX mode

If implemented, RWX must clearly declare its consistency model.

Potential API:

```yaml
parameters:
  accessSemantics: eventual
```

Documentation should explicitly say:

```text
Every node operates on a local filesystem replica.
Writes propagate asynchronously.
Simultaneous writes may create Syncthing conflicts.
No cross-node POSIX lock semantics are provided.
```

RWX should not silently look equivalent to NFS/CephFS.

---

## 45. Repository layout

Suggested Go repository:

```text
csi-driver-syncthing/
├── cmd/
│   ├── csi-controller/
│   ├── csi-node/
│   └── syncthing-node-agent/
├── pkg/
│   ├── csi/
│   │   ├── controller/
│   │   └── node/
│   ├── controller/
│   ├── syncthing/
│   │   ├── client/
│   │   ├── config/
│   │   └── status/
│   ├── api/
│   ├── mount/
│   └── metrics/
├── api/
│   └── v1alpha1/
├── config/
│   ├── crd/
│   ├── rbac/
│   ├── daemonset/
│   ├── controller/
│   └── storageclass/
├── deploy/
│   └── helm/
├── test/
│   ├── integration/
│   └── e2e/
├── docs/
│   ├── architecture.md
│   ├── consistency.md
│   ├── backup-restore.md
│   └── operations.md
└── README.md
```

---

## 46. Implementation rule of thumb

The cleanest separation is:

```text
CSI:
  decides WHEN a node needs a volume

Kubernetes CRDs:
  record WHICH nodes should participate

Syncthing node agent:
  reconciles HOW the local Syncthing instance participates

Syncthing:
  transfers FILE CONTENTS

CSI node plugin:
  exposes the LOCAL DIRECTORY to the Pod
```

No layer should silently take over another layer's responsibility.

---

## 47. Key architectural conclusions

1. The design is viable as a Kubernetes CSI driver for asynchronous replicated file data.
2. `ControllerPublishVolume` is the natural trigger for adding a node to Syncthing folder membership.
3. `ControllerUnpublishVolume` is the natural trigger for moving a replica into cache/removal lifecycle.
4. A node-local Syncthing agent is preferable to exposing Syncthing's REST API to the controller.
5. One PVC/PV should correspond to one stable Syncthing folder ID.
6. A bind mount is sufficient for actual Pod exposure.
7. Initial synchronization must be gated before normal mount.
8. Kubernetes, not Syncthing Introducers, should own desired replica topology.
9. A permanent receive-only external peer is a good disaster-recovery building block.
10. Replication is not backup; backup-side versioning/snapshots are still required.
11. Syncthing's asynchronous nature must be explicit in the StorageClass's supported semantics.
12. `ReadWriteOnce` should be the initial supported access mode.
13. Capacity/quota semantics require separate engineering and should not be implied by the first MVP.
14. Recovery metadata must survive outside the Kubernetes cluster if the external replica is intended to recover a totally lost cluster.

---

## 48. External references

Kubernetes:

- CSIDriver API: https://kubernetes.io/docs/reference/kubernetes-api/storage/csi-driver-v1/

Syncthing:

- REST API: https://docs.syncthing.net/dev/rest.html
- Config endpoints: https://docs.syncthing.net/rest/config
- System status: https://docs.syncthing.net/rest/system-status-get
- Database completion: https://docs.syncthing.net/v1.29.6/rest/db-completion-get.html
- Database/folder status: https://docs.syncthing.net/v1.30.0/rest/db-status-get.html
- Configuration and folder types: https://docs.syncthing.net/users/config.html

---

## 49. Proposed short project description

> **csi-driver-syncthing** is an experimental Kubernetes CSI driver that exposes node-local filesystem volumes replicated asynchronously by Syncthing. Kubernetes controls which worker nodes participate in each volume, while a per-node agent reconciles that desired topology into the local Syncthing configuration. Volumes are mounted into Pods using local bind mounts. The design targets single-writer file workloads first and optionally maintains permanent off-cluster Syncthing replicas for disaster recovery.
