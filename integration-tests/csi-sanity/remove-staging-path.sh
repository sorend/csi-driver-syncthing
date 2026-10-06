#!/bin/sh
set -eu

staging_path="$1"
rmdir "$(dirname "$staging_path")" 2>/dev/null || true
