#!/bin/sh
set -eu

target_path="$1"
target_parent="$(dirname "$target_path")"
mkdir -p "$target_parent"
printf '%s\n' "$target_parent"
