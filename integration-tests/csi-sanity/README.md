# csi-sanity on Kind

Run the CSI sanity suite on a local single-worker Kind cluster:

```sh
make csi-sanity
```

Requires Docker, Kind, kubectl, and Helm. The script builds and loads the driver and csi-sanity images, installs the chart and CRDs, then runs csi-sanity in a privileged pod on the Kind worker. The worker's kubelet directory is mounted into the test pod so it can reach both CSI sockets and exercise node mount operations. Test-created volumes use `initialSync=none` because the single-node environment has no peer to sync from; the chart's StorageClass configuration is not modified.

Use `make csi-sanity-clean` to delete the Kind cluster. Set `KIND_CLUSTER_NAME` or `CSI_SANITY_IMAGE` to override the defaults.
