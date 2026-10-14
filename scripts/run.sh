#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
CONFIG_FILE="${1:-config.yaml}"
CONFIG_PATH="$(realpath -m -- "$CONFIG_FILE")"
SERVICE_PID=''
# Only take over the exact repository executable and configuration.
if command -v systemctl >/dev/null && command -v ps >/dev/null &&
  timeout 3s systemctl is-active --quiet stock-watcher.service 2>/dev/null; then
  candidate="$(timeout 3s systemctl show --property=MainPID --value stock-watcher.service 2>/dev/null || true)"
  if [[ "$candidate" =~ ^[1-9][0-9]*$ ]] &&
    [[ "$(ps -p "$candidate" -o args= 2>/dev/null || true)" == "$ROOT_DIR/bin/stock-watcher run -config $CONFIG_PATH" ]]; then
    SERVICE_PID="$candidate"
  fi
fi
make build
if [[ -f .env ]]; then
  set -a
  source .env
  set +a
fi
if [[ -n "$SERVICE_PID" ]]; then
  # Validate without opening the shared writer or migrating its live ledger.
  ./bin/stock-watcher check-config -config "$CONFIG_FILE"
  printf 'Stopping stock-watcher.service (PID %s) before foreground startup...\n' "$SERVICE_PID"
  if ! timeout 30s systemctl stop stock-watcher.service; then
    echo 'Cannot stop stock-watcher.service; foreground startup aborted. Check service permissions and status.' >&2
    exit 1
  fi
  stopped_pid="$(timeout 3s systemctl show --property=MainPID --value stock-watcher.service)"
  if [[ "$stopped_pid" != 0 ]]; then
    echo 'stock-watcher.service has not stopped; foreground startup aborted.' >&2
    exit 1
  fi
  printf '%s\n' 'Starting in foreground. Ctrl+C stops this process; use systemctl start stock-watcher.service to return to background operation.'
fi
exec ./bin/stock-watcher run -config "$CONFIG_FILE"
