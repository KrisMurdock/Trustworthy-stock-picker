#!/usr/bin/env bash
# Exercise the real CLI without credentials or live market requests.
set -euo pipefail
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
command -v jq >/dev/null
command -v timeout >/dev/null
FIXTURE="$(mktemp -d)"
CONTAINER_NAME="stock-watcher-smoke-$$"
MODE=native
COMPOSE=()
cleanup() {
  if [[ "$MODE" == container ]]; then
    if [[ ${#COMPOSE[@]} -gt 0 ]]; then "${COMPOSE[@]}" down --timeout 10 >/dev/null 2>&1 || true; fi
    # Docker may create root-owned children when CI itself runs as a normal user.
    docker run --rm --network none --entrypoint /bin/sh \
      --mount "type=bind,src=$FIXTURE,dst=/fixture" "$IMAGE" \
      -c 'chmod -R a+rwX /fixture' >/dev/null 2>&1 || true
  fi
  rm -rf -- "$FIXTURE"
}
trap cleanup EXIT
mkdir "$FIXTURE/data" "$FIXTURE/logs"
cp "$ROOT_DIR/leaders.yaml" "$FIXTURE/leaders.yaml"
# Synthetic calendar is for storage checks, never trading decisions.
sed -e 's/require_cifs: true/require_cifs: false/' \
    -e "s/valid_from: '2026-01-01'/valid_from: '2020-01-01'/" \
    -e "s/valid_through: '2026-12-31'/valid_through: '2099-12-31'/" \
    "$ROOT_DIR/config.yaml.example" > "$FIXTURE/config.yaml"
if [[ "${1:-}" == --image ]]; then
  IMAGE="${2:?image required}"
  MODE=container
  CONFIG=/fixture/config.yaml
  DATA=/fixture/data
  RUN=(docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges:true
    --cpus 1 --memory 256m --pids-limit 64 --mount "type=bind,src=$FIXTURE,dst=/fixture"
    -e STOCK_WATCHER_DATA_DIR="$DATA" -e STOCK_WATCHER_LOG_DIR=/fixture/logs
    -e STOCK_WATCHER_LOG_COLOR=off "$IMAGE")
else
  CONFIG="$FIXTURE/config.yaml"
  DATA="$FIXTURE/data"
  RUN=(env -u FEISHU_WEBHOOK -u FEISHU_SECRET STOCK_WATCHER_DATA_DIR="$DATA"
    STOCK_WATCHER_LOG_DIR="$FIXTURE/logs" STOCK_WATCHER_LOG_COLOR=off
    STOCK_WATCHER_LOG_JSON=false STOCK_WATCHER_LOG_MAX_MB=10
    STOCK_WATCHER_LOG_MAX_DAYS=7 STOCK_WATCHER_LOG_BACKUPS=10 "${1:?binary required}")
fi
invoke() { timeout 30s "${RUN[@]}" "$@"; }
must_fail() {
  local code=0
  invoke "$@" > "$FIXTURE/failure.txt" 2>&1 || code=$?
  if [[ "$code" != 1 ]]; then
    echo "Expected application failure: $* (exit $code)" >&2
    cat "$FIXTURE/failure.txt" >&2
    exit 1
  fi
}
invoke version | tee "$FIXTURE/version.txt"
test -s "$FIXTURE/version.txt"
invoke init -config "$CONFIG" | tail -n 1 | jq -e '.status == "initialized"' >/dev/null
invoke check -config "$CONFIG" | tail -n 1 | jq -e '.status == "ready" and .pool_size > 0 and .notification_channels == 0' >/dev/null
invoke status -config "$CONFIG" | tail -n 1 | jq -e '.schema_version == 2' >/dev/null
invoke plan -config "$CONFIG" -code sz000001 -entry-price 10 > "$FIXTURE/plan.json"
jq -e '.schema_version == 1 and .reference_shares == 1900 and .reference_buy_cost < 20000 and .target_net_2_5_price > 10.25 and .target_net_3_price > .target_net_2_5_price and .stop_price < 10' "$FIXTURE/plan.json" >/dev/null
must_fail check -config "${CONFIG}.missing"
must_fail plan -config "$CONFIG" -code sz000001 -entry-price -1
mv "$FIXTURE/data" "$FIXTURE/saved-data"
must_fail check -config "$CONFIG"
test ! -e "$FIXTURE/data"
mv "$FIXTURE/saved-data" "$FIXTURE/data"
sed -i 's/require_cifs: false/require_cifs: true/' "$FIXTURE/config.yaml"
must_fail check -config "$CONFIG"
sed -i 's/require_cifs: true/require_cifs: false/' "$FIXTURE/config.yaml"
sed -i '/name: feishu-main/,/secret_env:/s/enabled: false/enabled: true/' "$FIXTURE/config.yaml"
must_fail check -config "$CONFIG"
printf '%s\n' 'webhook: https://open.feishu.cn/open-apis/bot/v2/hook/smoke-test-only' > "$FIXTURE/feishu.config"
sed -i '/name: feishu-main/a\      credentials_file: feishu.config' "$FIXTURE/config.yaml"
invoke check-config -config "$CONFIG" | tail -n 1 | jq -e '.status == "valid"' >/dev/null
mv "$FIXTURE/feishu.config" "$FIXTURE/feishu.saved"
must_fail check-config -config "$CONFIG"
mv "$FIXTURE/feishu.saved" "$FIXTURE/feishu.config"
if [[ "$MODE" == container ]]; then
  chmod 755 "$FIXTURE"
  chmod 644 "$FIXTURE/config.yaml" "$FIXTURE/leaders.yaml"
  chmod 600 "$FIXTURE/feishu.config"
  denied=0
  timeout 30s docker run --rm --network none --user 65534:65534 --read-only \
    --mount "type=bind,src=$FIXTURE,dst=/fixture,readonly" \
    "$IMAGE" check-config -config "$CONFIG" > "$FIXTURE/denied-credentials.txt" 2>&1 || denied=$?
  test "$denied" -eq 1
  grep -q 'cannot open Feishu credentials file' "$FIXTURE/denied-credentials.txt"
  if grep -q 'smoke-test-only' "$FIXTURE/denied-credentials.txt"; then exit 1; fi
fi
sed -i '/^      credentials_file: feishu.config$/d' "$FIXTURE/config.yaml"
sed -i '/name: feishu-main/,/secret_env:/s/enabled: true/enabled: false/' "$FIXTURE/config.yaml"
if [[ "$MODE" == container ]]; then
  chmod 755 "$FIXTURE"
  chmod 600 "$FIXTURE/config.yaml"
  timeout 30s docker run --rm --network none --user 65534:65534 --read-only \
    --entrypoint /bin/sh --mount "type=bind,src=$FIXTURE,dst=/fixture,readonly" \
    "$IMAGE" -c 'test -f /fixture/config.yaml && test ! -r /fixture/config.yaml'
  denied=0
  timeout 30s docker run --rm --network none --user 65534:65534 --read-only \
    --mount "type=bind,src=$FIXTURE,dst=/fixture,readonly" \
    "$IMAGE" check -config "$CONFIG" > "$FIXTURE/denied.txt" 2>&1 || denied=$?
  test "$denied" -eq 1
  grep -q 'cannot open configuration file' "$FIXTURE/denied.txt"
  # Use the deployed Compose definition; only network and probe interval differ.
  printf '%s\n' 'SMOKE_LITERAL=$not_expanded' > "$FIXTURE/runtime.env"
  cat > "$FIXTURE/compose.env" <<ENV
STOCK_WATCHER_IMAGE=$IMAGE
STOCK_WATCHER_HOST_DATA=$FIXTURE/data
STOCK_WATCHER_HOST_CONFIG=$FIXTURE/config.yaml
STOCK_WATCHER_HOST_POOL=$FIXTURE/leaders.yaml
STOCK_WATCHER_HOST_LOGS=$FIXTURE/logs
STOCK_WATCHER_RUNTIME_ENV=$FIXTURE/runtime.env
ENV
  cat > "$FIXTURE/override.yaml" <<'YAML'
services:
  stock-watcher:
    network_mode: none
    ports: !reset []
    healthcheck:
      interval: 1s
      timeout: 5s
      start_period: 1s
YAML
  # Deployment variables from the caller must never select production paths.
  COMPOSE=(env -u STOCK_WATCHER_IMAGE -u STOCK_WATCHER_VERSION -u STOCK_WATCHER_HOST_DATA
    -u STOCK_WATCHER_HOST_CONFIG -u STOCK_WATCHER_HOST_POOL -u STOCK_WATCHER_HOST_LOGS
    -u STOCK_WATCHER_RUNTIME_ENV docker compose --project-name "$CONTAINER_NAME"
    --env-file "$FIXTURE/compose.env" -f "$ROOT_DIR/docker/compose.yaml" -f "$FIXTURE/override.yaml")
  "${COMPOSE[@]}" config --quiet
  "${COMPOSE[@]}" up -d --no-build >/dev/null
  CONTAINER_ID="$("${COMPOSE[@]}" ps -q stock-watcher)"
  test -n "$CONTAINER_ID"
  healthy=false
  for ((i=0; i<30; i++)); do
    if [[ "$(docker inspect -f '{{.State.Health.Status}}' "$CONTAINER_ID")" == healthy ]]; then healthy=true; break; fi
    sleep 1
  done
  if [[ "$healthy" != true ]]; then
    docker logs "$CONTAINER_ID" >&2
    docker inspect -f '{{json .State.Health}}' "$CONTAINER_ID" >&2
    exit 1
  fi
  docker inspect "$CONTAINER_ID" | jq -e '.[0].Config.Env | index("SMOKE_LITERAL=$not_expanded") != null' >/dev/null
  "${COMPOSE[@]}" exec -T stock-watcher stock-watcher version | cmp - "$FIXTURE/version.txt"
  "${COMPOSE[@]}" exec -T stock-watcher stock-watcher status -config /app/config.yaml \
    | tail -n 1 | jq -e '.schema_version == 2' >/dev/null
  "${COMPOSE[@]}" stop --timeout 10 >/dev/null
  test "$(docker inspect -f '{{.State.ExitCode}}' "$CONTAINER_ID")" == 0
  docker logs "$CONTAINER_ID" 2>&1 | grep -q 'Stock watcher stopped'
  "${COMPOSE[@]}" down >/dev/null
  mv "$FIXTURE/data" "$FIXTURE/saved-data"
  missing=0
  timeout 30s "${COMPOSE[@]}" run --rm --no-deps stock-watcher check -config /app/config.yaml \
    > "$FIXTURE/missing-bind.txt" 2>&1 || missing=$?
  test "$missing" -ne 0 && test "$missing" -ne 124 && test "$missing" -ne 137
  test ! -e "$FIXTURE/data" # Compose must not create an empty local mount source.
fi
echo "Smoke passed ($MODE): ready/status/plan, missing config/data/credentials, CIFS guard; container also checks permissions, Compose health/raw env and SIGTERM."
