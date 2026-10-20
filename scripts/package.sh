#!/usr/bin/env bash
# 固定输出到仓库 .builds；只打包明确列出的运行材料。
set -euo pipefail
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
BUILD_GOARCH="${BUILD_GOARCH:-amd64}"
case "$BUILD_GOARCH" in amd64|arm64) ;; *) echo "Unsupported BUILD_GOARCH" >&2; exit 1;; esac
BUILD_VERSION="${VERSION:-$(git rev-parse HEAD)}"
[[ "$BUILD_VERSION" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "Invalid VERSION" >&2; exit 1; }
PACKAGE_PARENT="$ROOT_DIR/.builds/stock-watcher/linux"
mkdir -p "$PACKAGE_PARENT"
PACKAGE_STAGE="$(mktemp -d "$PACKAGE_PARENT/.package-XXXXXXXX")"
trap 'rm -rf -- "$PACKAGE_STAGE"' EXIT
mkdir -p "$PACKAGE_STAGE/bin" "$PACKAGE_STAGE/deploy" "$PACKAGE_STAGE/docs/assets"
CGO_ENABLED=0 GOOS=linux GOARCH="$BUILD_GOARCH" "${GO:-go}" build -trimpath -ldflags "-s -w -X main.version=$BUILD_VERSION" -o "$PACKAGE_STAGE/bin/stock-watcher" ./cmd/stock-watcher
cp leaders.yaml leaders.pending.yaml .env.example README.md "$PACKAGE_STAGE/"
cp deploy/stock-watcher.service "$PACKAGE_STAGE/deploy/"
cp docs/*.md "$PACKAGE_STAGE/docs/"
cp docs/assets/*.svg "$PACKAGE_STAGE/docs/assets/"
printf '%s\n' "$BUILD_VERSION" > "$PACKAGE_STAGE/VERSION"
git rev-parse HEAD > "$PACKAGE_STAGE/COMMIT"
(cd "$PACKAGE_STAGE" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS)
# 目标路径由固定前缀与校验后的架构组成，不接受任意删除路径。
rm -rf -- "$PACKAGE_PARENT/$BUILD_GOARCH"
mv -- "$PACKAGE_STAGE" "$PACKAGE_PARENT/$BUILD_GOARCH"
printf 'Package: .builds/stock-watcher/linux/%s\n' "$BUILD_GOARCH"
