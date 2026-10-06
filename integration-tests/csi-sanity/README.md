# csi-sanity on Kind

Run the complete CSI sanity suite on a local single-worker Kind cluster:

```sh
make csi-sanity
```

Requires Docker, Kind, kubectl, and Helm. The script builds and loads the driver and csi-sanity images, installs the chart and CRDs, then runs csi-sanity in a privileged pod on the Kind worker. The worker's kubelet directory is mounted into the test pod so it can reach both CSI sockets and exercise node mount operations. The test-specific chart values place the controller socket on that same worker node.

Use `make csi-sanity-clean` to delete the Kind cluster. Set `KIND_CLUSTER_NAME` or `CSI_SANITY_IMAGE` to override the defaults.
