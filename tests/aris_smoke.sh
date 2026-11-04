#!/usr/bin/env bash
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
if [[ ${1:-} == --image ]]; then
  image=${2:?image required}
  docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
    --tmpfs /state:uid=10001,gid=10001,mode=0700 --tmpfs /logs:uid=10001,gid=10001,mode=0700 \
    --mount "type=bind,src=$repo/examples/aris-registration.json,dst=/input.json,readonly" \
    --entrypoint /bin/sh "$image" -ec '
      test "$(id -u)" = 10001
      ! command -v python3
      ! command -v node
      aris-stock seal --request /input.json > /state/bundle.json
      set +e
      aris-stock run --request /state/bundle.json --state-dir /state --json
      result=$?
      set -e
      test "$result" -eq 3
      aris-stock verify --run example-proxy --state-dir /state --json
      test -s /logs/aris-stock.log
      test -s /state/runs/example-proxy/report.md
    '
  exit 0
fi
binary=${1:-"$repo/bin/aris-stock"}
scratch=$(mktemp -d /tmp/aris-smoke.XXXXXXXX)
cleanup() {
  case "$scratch" in /tmp/aris-smoke.*) rm -rf -- "$scratch" ;; esac
}
trap cleanup EXIT
"$binary" version | jq -e '.build_sha256 | test("^[a-f0-9]{64}$")' >/dev/null
"$binary" seal --request "$repo/examples/aris-registration.json" > "$scratch/bundle.json"
set +e
"$binary" run --request "$scratch/bundle.json" --state-dir "$scratch/state" --log-dir "$scratch/logs" --json > "$scratch/state.json"
result=$?
set -e
test "$result" -eq 3
jq -e '.status == "needs_human" and (.attempts|length) == 0' "$scratch/state.json" >/dev/null
"$binary" verify --run example-proxy --state-dir "$scratch/state" --log-dir "$scratch/logs" --json | jq -e '.verified' >/dev/null
set +e
"$binary" resume --run example-proxy --state-dir "$scratch/state" --log-dir "$scratch/logs" --json > /dev/null 2>&1
result=$?
set -e
test "$result" -eq 1
test -s "$scratch/logs/aris-stock.log"
printf '%s\n' 'aris-stock CLI smoke passed'
