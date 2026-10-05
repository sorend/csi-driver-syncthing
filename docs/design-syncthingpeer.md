# `SyncthingPeer` design

**Status:** Proposal  
**Audience:** CSI driver and operator implementers  
**Scope:** Represent off-cluster Syncthing instances as managed, durable replicas in the driver's replication topology.

## Summary

Add a cluster-scoped `SyncthingPeer` resource for a Syncthing instance that is not running on a Kubernetes worker. A peer can participate in selected volume folders, contribute to their replica target, and retain data while no Kubernetes workload is using the volume. Kubernetes worker replicas remain local mount targets; a `SyncthingPeer` is a replication backend and is never itself a CSI mount target.

Use **`SyncthingPeer`** as the resource name, with plural `syncthingpeers` and suggested short name `stpeer`. “Peer” describes the Syncthing role without assuming that the instance is physically outside a cluster or reachable by a particular network arrangement. “External” is a useful description in documentation, but is less precise as an API kind.

The central distinction is between:

- **workload replicas**: node-local copies that can be mounted by Pods and whose membership follows CSI attach/detach;
- **persistent peer replicas**: copies maintained on selected `SyncthingPeer` backends independently of Pod placement.

This allows the driver to remove a node-local copy when it is no longer required, while retaining a peer copy as a synchronization source and recovery location.

## Goals and non-goals

### Goals

- Represent multiple remote Syncthing instances in Kubernetes.
- Make remote instances usable as first-class participants in volume replication.
- Count healthy peer replicas toward a volume's requested replica target.
- Preserve selected peer copies across local detach and node-local replica cleanup.
- Interact with remote instances sufficiently to configure folders and report replica health.
- Keep remote API credentials out of `SyncthingPeer` specs and status.
- Make peer availability, identity changes, and reconciliation failures visible to operators.

### Non-goals

- Mount a remote peer directly into a Pod or expose it as a network filesystem.
- Turn asynchronous Syncthing replication into synchronous durability.
- Provide distributed filesystem consistency, locking, or multi-writer guarantees.
- Assume every remote instance is a backup. Syncthing replication alone does not protect against propagated deletion or corruption; peer-side versioning or snapshots are separate policy.

## Resource model

`SyncthingPeer` is cluster-scoped, like the existing `SyncthingNode` and `SyncthingVolume` resources. Its spec identifies the daemon and describes how the driver can configure it. Its status reports observed identity and connectivity. A representative API is:

```yaml
apiVersion: syncthing-storage.sorend.github.com/v1alpha1
kind: SyncthingPeer
metadata:
  name: offsite-a
  labels:
    syncthing-storage.sorend.github.com/location: region-a
spec:
  deviceID: "REMOTE-SYNCTHING-DEVICE-ID"
  addresses:
    - "tcp://sync.example.net:22000"
  api:
    url: "https://sync.example.net:8384"
    credentials:
      secretRef:
        name: offsite-a-api
        key: apiKey
  folderRoot: /srv/csi-volumes
  # Optional policy metadata for peer selection:
  failureDomain: region-a
status:
  ready: true
  observedDeviceID: "REMOTE-SYNCTHING-DEVICE-ID"
  syncthingVersion: "..."
  lastSeen: "..."
  conditions:
    - type: IdentityValid
      status: "True"
      reason: DeviceIDMatches
      message: The observed device ID matches spec.deviceID
      lastTransitionTime: "..."
```

Suggested fields:

| Field | Purpose |
| --- | --- |
| `spec.deviceID` | Expected immutable Syncthing device identity. A changed identity should require explicit operator action. |
| `spec.addresses[]` | Syncthing data-plane addresses configured for this device on cluster peers. |
| `spec.api.url` | HTTPS or otherwise protected endpoint used by the remote reconciler to manage the daemon. |
| `spec.api.credentials.secretRef` | Reference to a Kubernetes Secret containing the REST API key/token. |
| `spec.folderRoot` | Root directory on the remote host under which per-volume folders are stored. |
| `metadata.labels` / `spec.failureDomain` | Optional policy inputs for selecting replicas across locations or failure domains. |
| `status.ready` and `status.conditions` | Observed health; readiness should reflect successful API identity validation and usable configuration access, not merely a successful TCP connection. |

The Secret should be read only by the remote reconciler. It must not be copied into status, events, logs, or CSI volume context. The API endpoint should be independently configurable from the Syncthing device addresses: the REST API and data traffic can use distinct routes, DNS names, ports, and security controls.

### Ownership and access

An operator creates and manages `SyncthingPeer` resources and their referenced Secrets. The controller observes the resource and reconciles volume membership. A peer-side agent may be used instead of allowing the in-cluster controller to call the REST API, but that is an implementation choice; the CRD should not require the remote system to join the Kubernetes cluster or receive Kubernetes credentials.

An API endpoint reachable by the controller is needed if the controller is expected to configure remote folders/devices and observe their state. If only peer addresses and a device ID are supplied, the remote folder must be preconfigured out of band, which weakens reconciliation and status guarantees. The managed-API model is preferred for the first implementation.

## Volume replica API

The current `SyncthingVolume.spec.desiredReplicas` identifies participants using `nodeName`. Generalize participant identity to a typed reference so that both existing nodes and remote peers can be named without pretending that a peer is a Kubernetes node:

```yaml
spec:
  desiredReplicas:
    - targetRef:
        kind: SyncthingNode
        name: worker-a
      mode: active
    - targetRef:
        kind: SyncthingPeer
        name: offsite-a
      mode: persistent
```

`targetRef.kind` is restricted to supported participant kinds, initially `SyncthingNode` and `SyncthingPeer`. The API should enforce unique references within a volume. During migration, `nodeName` can remain accepted for node entries and be converted or normalized internally; a later API version can remove it once stored objects and clients have migrated.

The current replica modes are `active` and `cache`. They describe node attachment/retention behavior, and should not be overloaded to describe remote durability. A separate peer participation policy (shown above as `persistent`) makes it explicit that this membership survives local detach. If mode is retained as the common field, use a validated set that distinguishes target-specific behavior, for example `active`, `cache`, and `persistent`.

### Replica count and peer selection

Treat a replica target as a number of usable copies, not a count of configured devices. A copy counts toward the requested target only when its volume folder is configured and it meets the selected readiness criterion. Peer connectivity without a complete usable copy is not a ready replica.

For a first API iteration, volumes can list explicit peer references alongside the node references. This is deterministic and fits the existing desired-membership model. A higher-level policy can later select peers by labels and request a count, for example:

```yaml
spec:
  replication:
    totalReplicas: 2
    peerSelector:
      matchLabels:
        syncthing-storage.sorend.github.com/location: region-a
    minPeerReplicas: 1
```

If policy-based selection is introduced, define precisely whether `totalReplicas` includes the currently attached node, cached node replicas, and peer replicas. Recommended semantics are:

- one active workload node is a replica while attached;
- an eligible cached node copy may count only if it is healthy and policy permits it;
- each persistent `SyncthingPeer` copy counts when it satisfies the configured readiness requirement;
- additional nodes needed to support placement may temporarily raise the number of copies above the target;
- the controller must not remove a source copy until remaining copies satisfy the minimum and removal is safe.

Do not make the `SyncthingPeer` resource itself allocate every volume. Peer resources define available backend identities; per-volume references or a replication policy decide which volumes use them.

## Replica status

Volume status should use the same typed target identity as desired state, rather than representing all replicas with a `nodeName` field:

```yaml
status:
  replicas:
    - targetRef:
        kind: SyncthingNode
        name: worker-a
      state: Ready
      completion: 100
      connected: true
    - targetRef:
        kind: SyncthingPeer
        name: offsite-a
      state: Ready
      completion: 100
      connected: true
      lastTransitionTime: "..."
```

Useful states include `Pending`, `Configuring`, `Connecting`, `Syncing`, `Ready`, `Unavailable`, `Removing`, `Absent`, and `Error`. Keep per-volume folder and sync details in `SyncthingVolume.status`; put peer-wide health and identity information in `SyncthingPeer.status`. This separates “the remote daemon is reachable” from “this particular volume is fully replicated there.”

For `initialSync: wait`, a new local workload replica should wait for a usable data source and the configured initial synchronization gate. A peer that is selected as a source may qualify if its copy is healthy and contains the volume data. A peer that is disconnected or incomplete must not be treated as a ready copy merely to satisfy the replica count.

## Reconciliation architecture

### Remote reconciler

Add a controller-side reconciler for `SyncthingPeer` and/or volume membership that:

1. Resolves a volume's desired peer references and validates that each peer exists, is not deleting, and has a valid observed identity.
2. Configures the remote Syncthing device definitions and folders for participating cluster devices.
3. Uses the peer's `folderRoot` plus a safe volume identifier to select the remote path; never accepts an arbitrary per-volume absolute path from an untrusted volume object.
4. Reconciles the remote folder type and peer membership idempotently.
5. Reads remote connection, folder, and completion state and publishes per-volume replica status.
6. Retries transient API/network failures and reports actionable conditions without silently declaring an unavailable peer healthy.
7. Removes only configuration owned by this driver and only after the volume's deletion/retention policy authorizes cleanup.

The node agents continue to manage their own local Syncthing instances and local folders. They consume typed replica references and configure the selected `SyncthingPeer` device ID/address in the local Syncthing configuration. They do not receive remote API credentials.

### Consistent membership on every participant

Syncthing folder membership must converge on every participant. For a volume with `worker-a`, `worker-b`, and `offsite-a`, all three Syncthing instances need compatible device and folder membership. Adding the peer only to node configuration, without reconciling the remote folder, may leave the folder pending acceptance; configuring it only remotely leaves nodes unaware of the peer. Report a membership/configuration revision (or deterministic hash) to make partial convergence visible.

Reconciliation is eventually consistent; do not assume that updates across multiple REST APIs are atomic. On partial failure, retain the desired state, report the failed participant, and retry. Avoid removing a last known good source before a replacement is ready.

### Peer readiness and health

Distinguish these conditions:

- **peer ready**: the remote API is reachable, the observed device ID matches the configured identity, and the reconciler can inspect/configure it;
- **replica configured**: the remote folder and expected device membership exist;
- **replica ready**: data meets the volume's selected readiness threshold;
- **replica connected**: relevant Syncthing transport connections are currently established.

An offline peer may have a complete copy but cannot be assumed current. Expose connectivity and completion separately; policy decides whether a disconnected but previously complete peer can satisfy a durable replica minimum.

## Attach, detach, and cleanup behavior

### Attach

`ControllerPublishVolume` continues to add the scheduled `SyncthingNode` as an active workload replica and wait for the node's readiness policy. It should not attempt to attach a `SyncthingPeer` as the CSI node. The remote peer can serve as a source for initial sync when eligible. Before allowing an empty local folder to initialize, the controller must retain the current safeguard against silently creating an empty copy when prior data is known to exist.

### Detach

After the Pod is unpublished and controller detach removes the active node requirement, the local node replica can move to cache or be removed according to local retention policy. Persistent peer memberships remain desired and continue to receive updates. This is the key behavior that allows cluster nodes to unmount and eventually remove local data when no Pods require it without losing the off-cluster replica.

Do not require a remote peer to be reachable indefinitely for CSI detach to complete by default. Detach should be able to complete once local mount references are gone and the desired-state transition is recorded. If a durability policy requires confirmation that a peer has received the latest data, make that an explicit policy with bounded timeout and visible degraded status; asynchronous replication otherwise cannot promise that the remote copy contains the last writes.

### Peer or volume deletion

- Deleting a peer referenced by any volume should set a blocking condition or use a finalizer while references remain. It must not silently remove a required replica.
- Removing a peer from a volume is a desired membership change. Reconcile cleanup on both the peer and remaining participants, then publish `Absent` when removal is complete.
- Volume deletion follows CSI reclaim policy and explicit peer-retention settings. For `Retain`, preserve the remote folder and the metadata required to import/restore it. For `Delete`, remove remote data only when the policy authorizes it and cleanup is confirmed.
- Finalizers should prevent deletion from completing while required cleanup or retention decisions remain unresolved.

## Retention and backup semantics

Local `replicaRetention.afterDetach` and persistent peer retention solve different problems. Local retention controls how long an unused node keeps a cache. Peer membership controls whether a remote copy remains part of the volume topology. A persistent peer should normally remain selected after Pod detach; it must not inherit a local cache expiration by accident.

The remote Syncthing folder type is a policy decision. A `receiveonly` peer is a useful backup-oriented starting point, but it constrains where changes are accepted and needs explicit reconciliation semantics. `sendreceive` is more symmetric but permits remote edits to propagate back. The API should define a supported policy rather than exposing every Syncthing configuration field immediately. File versioning, snapshots, and immutable retention should be configured separately where backup protection is required.

## Security and networking

- Prefer HTTPS for the Syncthing REST API and validate certificates; do not add an insecure-TLS bypass as the default.
- Keep API credentials in Kubernetes Secrets and grant read access only to the controller/reconciler that needs them.
- Never put credentials in CR status, events, logs, metrics labels, CSI publish context, or node-agent configuration.
- Restrict data-plane and management-plane network access independently.
- The remote peer should not need Kubernetes API credentials for the controller-managed design.
- Validate `deviceID`, address schemes, URLs, and folder-root configuration. Do not permit a peer to cause folder configuration outside its configured root.
- Avoid treating a network-reachable REST endpoint as proof that the expected Syncthing identity is present; verify `/rest/system/status` against `spec.deviceID`.

## Failure behavior

| Failure | Expected behavior |
| --- | --- |
| Peer REST API unavailable | Mark peer/replicas unavailable or degraded; retry. Existing node-local I/O and healthy replicas continue. Do not remove the peer's desired membership because of a transient outage. |
| Syncthing data connection unavailable | Report `connected: false`; retain the last observed completion separately. Do not count a stale copy as current unless policy explicitly allows it. |
| Device ID differs from `spec.deviceID` | Set `IdentityChanged`, stop configuration against the unexpected daemon, and require operator correction. Do not silently adopt the new identity. |
| Secret missing or invalid | Report credential/API condition and retry after the Secret changes. Never include credential values in error messages. |
| Peer folder configuration partially applied | Keep desired membership, report configuration revision/error, and retry idempotently. Preserve known-good copies. |
| Peer deleted while referenced | Block deletion or mark references invalid; do not silently drop desired replica count. |
| No healthy source remains | Refuse to initialize an empty local replica when the volume is known to contain data; surface an explicit unavailable/data-source-lost error. |

## API evolution and migration

The current API has `DesiredReplica.nodeName` and `ReplicaStatus.nodeName`. Introducing remote targets requires changing both desired membership and observed status. Prefer a versioned migration over encoding peer names into fake Kubernetes node names.

An incremental path is:

1. Add `SyncthingPeer` and its CRD, status, RBAC, and controller registration.
2. Add `targetRef` support while continuing to accept `nodeName` for existing node entries.
3. Normalize legacy `nodeName` entries to `SyncthingNode` references in controller/agent logic.
4. Add remote reconciliation and peer-specific status; initially use explicit peer references to keep selection deterministic.
5. Add selector/count policy only after lifecycle and status semantics are proven.
6. Remove the legacy `nodeName` field only in a later served/storage API version with a conversion and migration plan.

Generated deepcopy code, CRD schemas, Helm RBAC, manager scheme/controller setup, agent read permissions, and examples must all be updated as implementation proceeds. The proposal itself does not change the installed API.

## Open decisions before implementation

1. Should the controller call the remote REST API, or should an optional remote-side agent own it?
2. What folder types are supported initially, and who is authoritative for remote-side edits?
3. Does the requested replica count include the active node, retained node caches, and remote peers? Define this before adding replica-count fields.
4. Can an offline but previously complete peer satisfy a minimum replica count, and how is staleness measured?
5. Should peer selection be explicit per volume first, or begin with labels/selectors and an automatic count?
6. Which readiness threshold gates attachment when the peer is the only available source?
7. What must be confirmed before deleting a peer folder under each PV reclaim policy?
8. How are peer-specific capacity and placement constraints surfaced? Requested capacity is currently advisory and the remote filesystem may have different limits.
9. How are remote credentials rotated and API TLS trust managed?
10. What supported Syncthing versions and REST endpoints are required for remote reconciliation?
