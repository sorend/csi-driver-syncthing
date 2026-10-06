#!/bin/sh
set -eu

target_path="$1"
rmdir "$(dirname "$target_path")" 2>/dev/null || true
