#!/usr/bin/env bash
# Start Stock Watcher on loopback and publish its read-only dashboard through a
# temporary Cloudflare Quick Tunnel. The public URL changes when the tunnel is
# recreated; use a named tunnel and owned domain for a permanent address.
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

readonly LOCAL_ADDR="${STOCK_WATCHER_PUBLIC_LOCAL_ADDR:-127.0.0.1:18080}"
readonly LOCAL_URL="http://${LOCAL_ADDR}"
readonly STATE_DIR="$ROOT_DIR/.scratch/public-tunnel"
readonly URL_FILE="$STATE_DIR/url"
readonly APP_LOG="$STATE_DIR/stock-watcher.log"
readonly TUNNEL_LOG="$STATE_DIR/cloudflared.log"

APP_PID=''
TUNNEL_PID=''

cleanup() {
  local status=$?
  trap - EXIT INT TERM HUP
  if [[ -n "$TUNNEL_PID" ]]; then
    kill -TERM "$TUNNEL_PID" 2>/dev/null || true
    wait "$TUNNEL_PID" 2>/dev/null || true
  fi
  if [[ -n "$APP_PID" ]]; then
    kill -TERM "$APP_PID" 2>/dev/null || true
    wait "$APP_PID" 2>/dev/null || true
  fi
  exit "$status"
}
trap cleanup EXIT INT TERM HUP

fail() {
  printf 'public tunnel: %s\n' "$*" >&2
  exit 1
}

public_tunnel_override_set=false
public_tunnel_override=''
if [[ ${STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED+x} ]]; then
  public_tunnel_override="$STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED"
  public_tunnel_override_set=true
fi
if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi
if [[ "$public_tunnel_override_set" == true ]]; then
  export STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED="$public_tunnel_override"
fi

public_tunnel_enabled="$(printf '%s' "${STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED:-false}" | tr '[:upper:]' '[:lower:]')"
case "$public_tunnel_enabled" in
  true|1|t) ;;
  false|0|f|'') fail 'public tunnel is disabled; set STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED=true to start it' ;;
  *) fail 'invalid STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED; use true or false' ;;
esac

if ! command -v cloudflared >/dev/null 2>&1; then
  fail 'cloudflared is required. On macOS run: brew install cloudflared'
fi

export STOCK_WATCHER_WEB_ADDR="$LOCAL_ADDR"
export STOCK_WATCHER_WEB_PUBLIC_READ_ONLY=true
export STOCK_WATCHER_LLM_WRITE_MODE=off
export STOCK_WATCHER_PREDICTOR_WRITE_MODE=off
export STOCK_WATCHER_PREDICTOR_PUSH_ENABLED=false
export STOCK_WATCHER_FEISHU_ENABLED=false

make build
./bin/stock-watcher check-config
./bin/stock-watcher init
./bin/stock-watcher check

mkdir -p "$STATE_DIR"
chmod 700 "$STATE_DIR"
rm -f "$URL_FILE" "$APP_LOG" "$TUNNEL_LOG"

./bin/stock-watcher run >"$APP_LOG" 2>&1 &
APP_PID=$!

wait_for_app() {
  local deadline=$((SECONDS + 90))
  while (( SECONDS < deadline )); do
    if ! kill -0 "$APP_PID" 2>/dev/null; then
      tail -n 80 "$APP_LOG" >&2 || true
      fail 'stock-watcher exited during startup'
    fi
    if curl --fail --silent --show-error --max-time 2 "$LOCAL_URL/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  tail -n 80 "$APP_LOG" >&2 || true
  fail 'stock-watcher did not become healthy within 90 seconds'
}

wait_for_app

cloudflared tunnel \
  --no-autoupdate \
  --loglevel info \
  --url "$LOCAL_URL" >"$TUNNEL_LOG" 2>&1 &
TUNNEL_PID=$!

wait_for_tunnel() {
  local deadline=$((SECONDS + 90))
  local url=''
  while (( SECONDS < deadline )); do
    if ! kill -0 "$TUNNEL_PID" 2>/dev/null; then
      tail -n 80 "$TUNNEL_LOG" >&2 || true
      fail 'cloudflared exited before publishing a URL'
    fi
    url="$(sed -nE 's#.*(https://[a-z0-9-]+\.trycloudflare\.com).*#\1#p' "$TUNNEL_LOG" | tail -n 1)"
    if [[ -n "$url" ]] && curl --fail --silent --show-error --max-time 10 "$url/healthz" >/dev/null 2>&1; then
      printf '%s\n' "$url" >"$URL_FILE"
      chmod 600 "$URL_FILE"
      printf '\nPublic read-only dashboard: %s/\n' "$url"
      printf 'Public URL saved to: %s\n' "$URL_FILE"
      printf 'Keep this terminal running. The temporary URL changes when this tunnel restarts.\n\n'
      return 0
    fi
    sleep 1
  done
  tail -n 80 "$TUNNEL_LOG" >&2 || true
  fail 'cloudflared did not publish a healthy URL within 90 seconds'
}

wait_for_tunnel

# Keep both processes in one lifecycle. If either exits, stop the other instead
# of leaving a local dashboard running without its public route.
while true; do
  if ! kill -0 "$APP_PID" 2>/dev/null; then
    wait "$APP_PID" || true
    tail -n 80 "$APP_LOG" >&2 || true
    fail 'stock-watcher stopped'
  fi
  if ! kill -0 "$TUNNEL_PID" 2>/dev/null; then
    wait "$TUNNEL_PID" || true
    tail -n 80 "$TUNNEL_LOG" >&2 || true
    fail 'cloudflared stopped; restart the command to obtain a new temporary URL'
  fi
  sleep 2
done
