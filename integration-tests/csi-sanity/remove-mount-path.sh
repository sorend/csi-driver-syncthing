#!/bin/sh
set -eu

# TargetPath is the directory returned by create-mount-path.sh. NodeUnpublishVolume
# already removed the "<TargetPath>/target" child, so only the parent is left.
target_path="$1"
rmdir "$target_path" 2>/dev/null || true
