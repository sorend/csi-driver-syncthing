# Kubernetes storage e2e on Kind

Run the upstream Kubernetes external CSI storage suite against this driver:

```sh
make kubernetes-storage-e2e
```

The runner downloads the `e2e.test` binary from the pinned Kubernetes test release (`v1.34.0`), creates a Kind cluster with one control-plane and two worker nodes, builds and loads the driver image, installs the Helm chart and CRDs, and waits for the driver pods to become ready. The test driver definition points upstream tests at the pre-installed `syncthing-e2e` StorageClass, which uses `initialSync: none` so single-node volume lifecycle tests do not wait for a replication peer.

The run selects every upstream test the driver can serve: the `Dynamic PV (default fs)` and `Dynamic PV (filesystem volmode)` test patterns for `csi.syncthing.io`, covering provisioning, mounting, `subPath`, `multiVolume`, `volumeMode`, `volumeIO` and data persistence. Feature-tagged and disruptive tests are skipped.

Three filters keep the selection honest rather than merely green:

* `testdriver.yaml` declares only the capabilities the driver implements, so upstream skips the tests for features it lacks instead of failing them. Absent capabilities cover raw block, cloning (`pvcDataSource`), snapshots, volume expansion, `ReadWriteOncePod`, `RWX`, `volumeLimits` and topology.
* The focus regex ends at the closing bracket of `[Testpattern: ...]` so the `(allowExpansion)` variants, which the driver cannot serve, are not matched.
* `should provision storage with any volume data source` is skipped explicitly. It requires a CSI driver with inline ephemeral volume support and is the one selected spec upstream leaves unguarded, so no capability can switch it off. It is also labelled `[Serial]`, which means a failure would abort every remaining spec.

Reports are written to `artifacts/kubernetes-storage-e2e-full/` (ignored by git). On test failure the script also saves Kubernetes objects, pod logs, events, and exported Kind logs there.

Run only the single `volumes should store data` lifecycle test with:

```sh
make kubernetes-storage-e2e-focused
```

That is a quick smoke test of provisioning, mounting, writes and data persistence across pod recreation. It uses the same cluster and test-driver setup and writes to `artifacts/kubernetes-storage-e2e/`. `make kubernetes-storage-e2e-full` remains as an alias for the default target. The corresponding GitHub Actions workflow runs on pushes and pull requests, can also be started manually, and allows up to six hours.

Requires Docker, Kind, kubectl, Helm, curl, and tar. The runner creates the cluster if it does not already exist. Delete it with:

```sh
make kubernetes-storage-e2e-clean
```

Defaults can be overridden with `KUBERNETES_VERSION`, `KIND_NODE_IMAGE`, `KUBERNETES_STORAGE_E2E_CLUSTER_NAME`, and `CSI_STORAGE_E2E_ARTIFACTS` make variables. Keep `KUBERNETES_VERSION` and the Kind node image Kubernetes version aligned.
