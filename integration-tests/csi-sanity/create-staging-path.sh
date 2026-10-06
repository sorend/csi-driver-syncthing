#!/bin/sh
set -eu

staging_path="$1"
staging_root="$(dirname "$(dirname "$staging_path")")"
mkdir -p "$staging_root"
staging_dir="$(mktemp -d "$staging_root/csi-sanity.XXXXXX")"
mkdir "$staging_dir/globalmount"
printf '%s/globalmount\n' "$staging_dir"
