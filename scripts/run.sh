#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
SERVICE_PID=''
# Only take over the exact repository executable with the built-in configuration.
if command -v systemctl >/dev/null && command -v ps >/dev/null &&
  timeout 3s systemctl is-active --quiet stock-watcher.service 2>/dev/null; then
  candidate="$(timeout 3s systemctl show --property=MainPID --value stock-watcher.service 2>/dev/null || true)"
  if [[ "$candidate" =~ ^[1-9][0-9]*$ ]] &&
    [[ "$(ps -p "$candidate" -o args= 2>/dev/null || true)" == "$ROOT_DIR/bin/stock-watcher run" ]]; then
    SERVICE_PID="$candidate"
  fi
fi
make build
public_tunnel_override_set=false
public_tunnel_override=''
if [[ ${STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED+x} ]]; then
  public_tunnel_override="$STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED"
  public_tunnel_override_set=true
fi
if [[ -f .env ]]; then
  set -a
  source .env
  set +a
fi
if [[ "$public_tunnel_override_set" == true ]]; then
  export STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED="$public_tunnel_override"
fi
public_tunnel_enabled="$(printf '%s' "${STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED:-false}" | tr '[:upper:]' '[:lower:]')"
case "$public_tunnel_enabled" in
  true|1|t) ;;
  false|0|f|'') ;;
  *) printf 'Invalid STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED; use true or false.\n' >&2; exit 1 ;;
esac
if [[ -n "$SERVICE_PID" ]]; then
  # Validate without opening the shared writer or migrating its live ledger.
  ./bin/stock-watcher check-config
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
if [[ "$public_tunnel_enabled" =~ ^(true|1|t)$ ]]; then
  exec bash scripts/public-tunnel.sh
fi
exec ./bin/stock-watcher run
