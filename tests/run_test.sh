#!/usr/bin/env bash
# Exercise make run in an isolated checkout; no real service, data or credentials.
set -euo pipefail
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
REAL_MAKE="$(command -v make)"
FIXTURE="$(mktemp -d)"
trap 'rm -rf -- "$FIXTURE"' EXIT
mkdir -p "$FIXTURE/scripts" "$FIXTURE/bin" "$FIXTURE/mock"
cp "$ROOT_DIR/Makefile" "$FIXTURE/"
cp "$ROOT_DIR/scripts/run.sh" "$FIXTURE/scripts/"
touch "$FIXTURE/config.yaml" "$FIXTURE/other.yaml"
cat > "$FIXTURE/mock/systemctl" <<'SH'
#!/usr/bin/env bash
[[ "${MOCK_SLOW:-0}" == 0 ]] || sleep 10
[[ "${MOCK_DENIED:-0}" == 0 ]] || exit 1
case "$1" in
  is-active) [[ "$MOCK_ACTIVE" == 1 ]];;
  show) echo 12345;;
  *) exit 1;;
esac
SH
cat > "$FIXTURE/mock/ps" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$MOCK_COMMAND"
SH
cat > "$FIXTURE/mock/go" <<'SH'
#!/usr/bin/env bash
printf 'build\n' >> "$TRACE"
exit "${MOCK_BUILD_EXIT:-0}"
SH
cat > "$FIXTURE/bin/stock-watcher" <<'SH'
#!/usr/bin/env bash
printf 'exec %s\n' "$*" >> "$TRACE"
if [[ "$MOCK_ACTIVE" == 1 && "$*" == 'run -config config.yaml' && "$MOCK_COMMAND" == "$FIXTURE/bin/stock-watcher run -config $FIXTURE/config.yaml" ]]; then
  echo 'another stock-watcher process owns the data directory' >&2
  exit 1
fi
[[ -f "$3" ]] || { echo 'cannot open configuration file' >&2; exit 1; }
printf 'env %s\n' "${RUN_TEST_VALUE:-unset}" >> "$TRACE"
SH
chmod +x "$FIXTURE/mock/"* "$FIXTURE/bin/stock-watcher"
export FIXTURE TRACE="$FIXTURE/trace" MOCK_ACTIVE=1
export MOCK_COMMAND="$FIXTURE/bin/stock-watcher run -config $FIXTURE/config.yaml"
export PATH="$FIXTURE/mock:$PATH"
run_make() {
  : > "$TRACE"
  "$REAL_MAKE" --no-print-directory -C "$FIXTURE" run VERSION=test "$@" > "$FIXTURE/output" 2>&1
}
if ! run_make; then cat "$FIXTURE/output" >&2; exit 1; fi
test ! -s "$TRACE" # Neither rebuild nor launch when the same service is running.
grep -q 'already running' "$FIXTURE/output"
grep -q 'systemctl restart stock-watcher.service' "$FIXTURE/output"
run_make CONFIG=other.yaml
grep -q '^build$' "$TRACE"
grep -q '^exec run -config other.yaml$' "$TRACE"
# Another checkout's service must not hide startup of this checkout.
MOCK_COMMAND='/elsewhere/bin/stock-watcher run -config /elsewhere/config.yaml' run_make
grep -q '^exec run -config config.yaml$' "$TRACE"
MOCK_ACTIVE=0 run_make
grep -q '^build$' "$TRACE"
grep -q '^exec run -config config.yaml$' "$TRACE"
# Failed/slow systemd inspection must fall through to the actual CLI, never claim success.
MOCK_COMMAND='' MOCK_DENIED=1 run_make
grep -q '^exec run -config config.yaml$' "$TRACE"
MOCK_COMMAND='' MOCK_SLOW=1 run_make
grep -q '^exec run -config config.yaml$' "$TRACE"
if MOCK_ACTIVE=0 run_make CONFIG=missing.yaml; then echo 'Missing config unexpectedly succeeded' >&2; exit 1; fi
grep -q 'cannot open configuration file' "$FIXTURE/output"
printf '%s\n' 'RUN_TEST_VALUE=loaded' > "$FIXTURE/.env"
MOCK_ACTIVE=0 run_make
grep -q '^env loaded$' "$TRACE"
if MOCK_ACTIVE=0 MOCK_BUILD_EXIT=7 run_make; then echo 'Build failure unexpectedly succeeded' >&2; exit 1; fi
test "$(cat "$TRACE")" == build
echo 'Run entrypoint passed: active service, other config/checkout, inactive/denied/timeout, missing config and dotenv.'
