# Kubernetes storage e2e on Kind

Run the upstream Kubernetes external CSI storage suite against this driver:

```sh
make kubernetes-storage-e2e
```

The runner downloads the `e2e.test` binary from the pinned Kubernetes test release (`v1.34.0`), creates a Kind cluster with one control-plane and two worker nodes, builds and loads the driver image, installs the Helm chart and CRDs, and waits for the driver pods to become ready. The test driver definition points upstream tests at the pre-installed `syncthing-e2e` StorageClass, which uses `initialSync: none` so single-node volume lifecycle tests do not wait for a replication peer.

The run focuses on the upstream `volumes should store data` test for `csi.syncthing.io`, which checks dynamic provisioning, mounting, writes, and data persistence across pod recreation. Other tests are not selected because the upstream suite includes cases that this driver's current cleanup behavior does not support. Feature-tagged or disruptive tests are skipped as well. Reports are written to `artifacts/kubernetes-storage-e2e/` (ignored by git). On test failure the script also saves Kubernetes objects, pod logs, events, and exported Kind logs there.

Run all upstream external storage tests selected for this driver with:

```sh
make kubernetes-storage-e2e-full
```

The full suite uses the same cluster and test-driver setup, selects all `External.Storage` tests for `csi.syncthing.io`, and skips feature-tagged and disruptive tests. Reports and diagnostics use the separate `artifacts/kubernetes-storage-e2e-full/` directory. The corresponding GitHub Actions workflow runs on pushes and pull requests, can also be started manually, and allows up to six hours.

Requires Docker, Kind, kubectl, Helm, curl, and tar. The runner creates the cluster if it does not already exist. Delete it with:

```sh
make kubernetes-storage-e2e-clean
```

Defaults can be overridden with `KUBERNETES_VERSION`, `KIND_NODE_IMAGE`, `KUBERNETES_STORAGE_E2E_CLUSTER_NAME`, and `CSI_STORAGE_E2E_ARTIFACTS` make variables. Keep `KUBERNETES_VERSION` and the Kind node image Kubernetes version aligned.
