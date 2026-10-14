#!/usr/bin/env bash
set -euo pipefail
project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
binary="$project_dir/bin/stock-watcher"
if [[ ! -x "$binary" ]]; then
  printf '%s\n' 'Build the native Go binary first: make build' >&2
  exit 1
fi
exec "$binary" run -config "${1:-$project_dir/config.yaml}"
