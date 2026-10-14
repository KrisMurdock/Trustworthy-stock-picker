#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
CONFIG_FILE="${1:-config.yaml}"
CONFIG_PATH="$(realpath -m -- "$CONFIG_FILE")"
# systemd owns the writer in deployed checkouts. Only reuse the exact same
# executable/configuration; other checkouts and custom configs still launch.
if command -v systemctl >/dev/null && command -v ps >/dev/null &&
  timeout 3s systemctl is-active --quiet stock-watcher.service 2>/dev/null; then
  SERVICE_PID="$(timeout 3s systemctl show --property=MainPID --value stock-watcher.service 2>/dev/null || true)"
  if [[ "$SERVICE_PID" =~ ^[1-9][0-9]*$ ]] &&
    [[ "$(ps -p "$SERVICE_PID" -o args= 2>/dev/null || true)" == "$ROOT_DIR/bin/stock-watcher run -config $CONFIG_PATH" ]]; then
    printf 'stock-watcher.service is already running (PID %s).\n' "$SERVICE_PID"
    printf '%s\n' 'No rebuild or second process was started. Configuration/environment changes need a service restart.' \
      'Status: systemctl status stock-watcher.service --no-pager' \
      'Logs: journalctl -u stock-watcher.service -f' \
      'Restart: systemctl restart stock-watcher.service' \
      'Foreground: systemctl stop stock-watcher.service && make run'
    exit 0
  fi
fi
make build
if [[ -f .env ]]; then
  set -a
  source .env
  set +a
fi
exec ./bin/stock-watcher run -config "$CONFIG_FILE"
