#!/usr/bin/env bash
# Exercise the public wrapper with mocked build, runtime and tunnel processes.
set -euo pipefail
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
FIXTURE="$(mktemp -d)"
WRAPPER_PID=''
APP_PID=''
TUNNEL_PID=''

cleanup() {
  local status=$?
  trap - EXIT INT TERM HUP
  for pid in "$WRAPPER_PID" "$TUNNEL_PID" "$APP_PID"; do
    if [[ -n "$pid" ]]; then
      kill -TERM "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  rm -rf -- "$FIXTURE"
  exit "$status"
}
trap cleanup EXIT INT TERM HUP

mkdir -p "$FIXTURE/scripts" "$FIXTURE/bin" "$FIXTURE/mock"
cp "$ROOT_DIR/scripts/public-tunnel.sh" "$FIXTURE/scripts/"
printf '%s\n' \
  'STOCK_WATCHER_WEB_PUBLIC_READ_ONLY=false' \
  'STOCK_WATCHER_PREDICTOR_WRITE_MODE=all' \
  'STOCK_WATCHER_PREDICTOR_PUSH_ENABLED=true' \
  'STOCK_WATCHER_FEISHU_ENABLED=true' > "$FIXTURE/.env"

cat > "$FIXTURE/mock/make" <<'SH'
#!/usr/bin/env bash
printf 'make %s\n' "$*" >> "$TRACE"
SH
cat > "$FIXTURE/mock/curl" <<'SH'
#!/usr/bin/env bash
printf 'curl %s\n' "$*" >> "$TRACE"
exit 0
SH
cat > "$FIXTURE/mock/cloudflared" <<'SH'
#!/usr/bin/env bash
printf 'cloudflared %s\n' "$*" >> "$TRACE"
printf '%s\n' "$$" > "$FIXTURE/tunnel.pid"
printf '%s\n' 'INF | https://public-test.trycloudflare.com'
exec sleep 60
SH
cat > "$FIXTURE/bin/stock-watcher" <<'SH'
#!/usr/bin/env bash
printf 'stock-watcher %s\n' "$*" >> "$TRACE"
if [[ "$1" == run ]]; then
  printf '%s\n' "$$" > "$FIXTURE/app.pid"
  printf '%s\n' "$STOCK_WATCHER_WEB_ADDR" "$STOCK_WATCHER_WEB_PUBLIC_READ_ONLY" "$STOCK_WATCHER_LLM_WRITE_MODE" "$STOCK_WATCHER_PREDICTOR_WRITE_MODE" "$STOCK_WATCHER_PREDICTOR_PUSH_ENABLED" "$STOCK_WATCHER_FEISHU_ENABLED" > "$FIXTURE/runtime-env"
  exec sleep 60
fi
SH
chmod +x "$FIXTURE/mock/"* "$FIXTURE/bin/stock-watcher"

TRACE="$FIXTURE/trace"
export TRACE FIXTURE
export PATH="$FIXTURE/mock:$PATH"
export STOCK_WATCHER_WEB_PUBLIC_READ_ONLY=false
export STOCK_WATCHER_LLM_WRITE_MODE=all
export STOCK_WATCHER_PREDICTOR_WRITE_MODE=all
export STOCK_WATCHER_PREDICTOR_PUSH_ENABLED=true
export STOCK_WATCHER_FEISHU_ENABLED=true

bash "$FIXTURE/scripts/public-tunnel.sh" > "$FIXTURE/output" 2>&1 &
WRAPPER_PID=$!

URL_FILE="$FIXTURE/.scratch/public-tunnel/url"
for _ in $(seq 1 100); do
  [[ -s "$URL_FILE" ]] && break
  kill -0 "$WRAPPER_PID" 2>/dev/null || { cat "$FIXTURE/output" >&2; exit 1; }
  sleep 0.05
done
[[ -s "$URL_FILE" ]] || { cat "$FIXTURE/output" >&2; exit 1; }

[[ "$(cat "$URL_FILE")" == 'https://public-test.trycloudflare.com' ]]
expected_env=$'127.0.0.1:18080\ntrue\noff\noff\nfalse\nfalse'
[[ "$(cat "$FIXTURE/runtime-env")" == "$expected_env" ]]
grep -q '^make build$' "$TRACE"
grep -q '^stock-watcher check-config$' "$TRACE"
grep -q '^stock-watcher init$' "$TRACE"
grep -q '^stock-watcher check$' "$TRACE"
grep -q '^stock-watcher run$' "$TRACE"
grep -q '^cloudflared tunnel .*--url http://127.0.0.1:18080$' "$TRACE"
grep -q '^curl .*http://127.0.0.1:18080/healthz' "$TRACE"
grep -q '^curl .*https://public-test.trycloudflare.com/healthz' "$TRACE"
grep -q 'Public read-only dashboard: https://public-test.trycloudflare.com/' "$FIXTURE/output"

APP_PID="$(cat "$FIXTURE/app.pid")"
TUNNEL_PID="$(cat "$FIXTURE/tunnel.pid")"
kill -TERM "$WRAPPER_PID"
wait "$WRAPPER_PID" || true
WRAPPER_PID=''

for _ in $(seq 1 50); do
  ! kill -0 "$APP_PID" 2>/dev/null && ! kill -0 "$TUNNEL_PID" 2>/dev/null && break
  sleep 0.05
done
! kill -0 "$APP_PID" 2>/dev/null
! kill -0 "$TUNNEL_PID" 2>/dev/null
APP_PID=''
TUNNEL_PID=''

echo 'Public tunnel passed: startup order, forced read-only mode, URL and child cleanup.'
