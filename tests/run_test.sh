#!/usr/bin/env bash
# Real Make/wrapper, mocked service and CLI; never touches production processes.
set -euo pipefail
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
REAL_MAKE="$(command -v make)"
FIXTURE="$(mktemp -d)"
trap 'rm -rf -- "$FIXTURE"' EXIT
mkdir -p "$FIXTURE/scripts" "$FIXTURE/bin" "$FIXTURE/mock"
cp "$ROOT_DIR/Makefile" "$FIXTURE/"
cp "$ROOT_DIR/scripts/run.sh" "$FIXTURE/scripts/"
cat > "$FIXTURE/mock/systemctl" <<'SH'
#!/usr/bin/env bash
[[ "${MOCK_SLOW:-0}" == 0 ]] || sleep 10
[[ "${MOCK_DENIED:-0}" == 0 ]] || exit 1
case "$1" in
  is-active) [[ "$MOCK_ACTIVE" == 1 && ! -f "$FIXTURE/stopped" ]];;
  show) if [[ -f "$FIXTURE/stopped" ]]; then echo 0; else echo 12345; fi;;
  stop)
    printf 'stop\n' >> "$TRACE"
    [[ "${MOCK_STOP_FAIL:-0}" == 0 ]] || exit 1
    [[ "${MOCK_STILL_RUNNING:-0}" == 1 ]] || touch "$FIXTURE/stopped"
    ;;
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
if [[ "$1" == check-config ]]; then exit "${MOCK_CONFIG_EXIT:-0}"; fi
if [[ "$MOCK_ACTIVE" == 1 && ! -f "$FIXTURE/stopped" && "$*" == 'run' && "$MOCK_COMMAND" == "$FIXTURE/bin/stock-watcher run" ]]; then
  echo 'another stock-watcher process owns the data directory' >&2
  exit 1
fi
printf 'env %s\n' "${RUN_TEST_VALUE:-unset}" >> "$TRACE"
SH
chmod +x "$FIXTURE/mock/"* "$FIXTURE/bin/stock-watcher"
export FIXTURE TRACE="$FIXTURE/trace" MOCK_ACTIVE=1
export MOCK_COMMAND="$FIXTURE/bin/stock-watcher run"
export PATH="$FIXTURE/mock:$PATH"
run_make() {
  : > "$TRACE"
  rm -f "$FIXTURE/stopped"
  "$REAL_MAKE" --no-print-directory -C "$FIXTURE" run VERSION=test "$@" > "$FIXTURE/output" 2>&1
}
if ! run_make; then cat "$FIXTURE/output" >&2; exit 1; fi
expected=$'build\nexec check-config\nstop\nexec run\nenv unset'
if [[ "$(cat "$TRACE")" != "$expected" ]]; then
  echo 'Expected a real foreground handoff after build and configuration validation' >&2
  cat "$TRACE" >&2; cat "$FIXTURE/output" >&2; exit 1
fi
# Dry-run must never build, stop a service or start a process.
: > "$TRACE"
"$REAL_MAKE" --no-print-directory -C "$FIXTURE" -n run > "$FIXTURE/dry-run"
test ! -s "$TRACE"
test "$(cat "$FIXTURE/dry-run")" == 'bash scripts/run.sh'
MOCK_COMMAND='/elsewhere/bin/stock-watcher run' run_make
! grep -q '^stop$' "$TRACE"
MOCK_ACTIVE=0 run_make
grep -q '^exec run$' "$TRACE"
MOCK_COMMAND='' MOCK_DENIED=1 run_make
grep -q '^exec run$' "$TRACE"
MOCK_COMMAND='' MOCK_SLOW=1 run_make
grep -q '^exec run$' "$TRACE"
if MOCK_STOP_FAIL=1 run_make; then echo 'Stop failure ignored' >&2; exit 1; fi
! grep -q '^exec run ' "$TRACE"
if MOCK_STILL_RUNNING=1 run_make; then echo 'Still running service ignored' >&2; exit 1; fi
! grep -q '^exec run ' "$TRACE"
if MOCK_CONFIG_EXIT=1 run_make; then echo 'Invalid config accepted' >&2; exit 1; fi
! grep -q '^stop$' "$TRACE"
if MOCK_BUILD_EXIT=7 run_make; then echo 'Build failure ignored' >&2; exit 1; fi
test "$(cat "$TRACE")" == build
printf '%s\n' 'RUN_TEST_VALUE=loaded' > "$FIXTURE/.env"
run_make
grep -q '^env loaded$' "$TRACE"
echo 'Run passed: real handoff ordering, build/config/stop failures, dry-run, alternate service, denied/timeout and dotenv.'
