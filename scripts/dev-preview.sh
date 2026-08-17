#!/usr/bin/env bash
# Dev-preview launcher: the control binary alone (embedded dist) on :8787 with
# the NIGHTSHIFT_DEV seed HOME. Used by .claude/launch.json for local preview;
# `make dev` remains the live-reload path (vite :5173).
set -euo pipefail
cd "$(dirname "$0")/.."
make control-bin >/dev/null
mkdir -p control/.devhome
exec env HOME="$(pwd)/control/.devhome" NIGHTSHIFT_DEV=1 LISTEN=127.0.0.1:8787 ./control/control
