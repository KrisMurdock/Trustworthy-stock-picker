#!/usr/bin/env bash
# 本地和 CI 共用的 Go 工程门禁，不读取私有 .env，不连接行情或飞书。
set -euo pipefail
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
make fmt-check vet test build smoke package
(cd ".builds/stock-watcher/linux/${BUILD_GOARCH:-amd64}" && sha256sum -c SHA256SUMS)
