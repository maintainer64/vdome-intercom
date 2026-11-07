#!/bin/sh
set -eu
cd -- "$(dirname -- "$0")/.."
export VDOME_ADDR="${VDOME_ADDR:-127.0.0.1:8081}"
export VDOME_ORIGIN="${VDOME_ORIGIN:-http://127.0.0.1:8081}"
export VDOME_PROVIDER="${VDOME_PROVIDER:-live}"
exec ./bin/vdome-server
