#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
if [[ -f .env ]]; then
  set -a
  source .env
  set +a
fi
exec ./bin/stock-watcher run -config "${1:-config.yaml}"
