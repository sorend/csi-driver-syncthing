#!/bin/sh
set -eu

# csi-sanity treats stdout as the new TargetPath and publishes at
# "<TargetPath>/target", so this must return the directory it created rather
# than its parent. The resulting path has to match the layout kubelet uses for
# CSI volumes: /var/lib/kubelet/pods/<uid>/volumes/kubernetes.io~csi/<name>/mount
target_path="$1"
mkdir -p "$target_path"
printf '%s\n' "$target_path"
