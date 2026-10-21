VERSION ?= $(shell git rev-parse HEAD)
GO ?= go
IMAGE ?= stock-watcher:$(VERSION)
BUILD_GOARCH ?= amd64
.DEFAULT_GOAL := help

.PHONY: help fmt-check test vet build run check status minute-backfill smoke sources-smoke intraday-smoke dashboard-smoke review-smoke docker-dashboard-smoke docker-review-smoke ci package image docker-build docker-smoke clean

help:
	@printf '%s\n' 'make build / run / check / status  构建与本地运行（内置配置）' 'make run                        同时启动监控与网页 http://127.0.0.1:8080' 'STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED=true make run  显式开启临时 Cloudflare 公网只读入口' 'make minute-backfill            补齐历史 5 分钟线（MINUTE_ARGS 可覆盖参数）' 'make ci                         格式、静态检查、竞态测试、CLI 冒烟、发布包' 'make image / docker-smoke       构建镜像 / 验证容器（IMAGE、VERSION 可覆盖）' 'make package                   Linux 发布包（BUILD_GOARCH=amd64 或 arm64）' 'make clean                     清理 bin 和 .builds'

fmt-check:
	@files="$$(gofmt -l cmd internal)"; if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o bin/stock-watcher ./cmd/stock-watcher

run:
	bash scripts/run.sh

check: build
	./bin/stock-watcher check

status: build
	./bin/stock-watcher status

MINUTE_ARGS ?= -workers 6
minute-backfill: build
	./bin/stock-watcher backfill-minutes $(MINUTE_ARGS)

smoke: build
	bash tests/run_test.sh
	bash tests/public_tunnel_test.sh
	python3 tests/calculate_strategy_test.py
	python3 tests/strategy_model_test.py
	bash tests/smoke.sh "$(CURDIR)/bin/stock-watcher"

dashboard-smoke: build
	python3 tests/dashboard_smoke.py "$(CURDIR)/bin/stock-watcher"

review-smoke: build
	python3 tests/review_smoke.py "$(CURDIR)/bin/stock-watcher"

intraday-smoke: build
	python3 tests/intraday_smoke.py "$(CURDIR)/bin/stock-watcher"

sources-smoke: build
	python3 tests/sources_smoke.py "$(CURDIR)/bin/stock-watcher"

docker-dashboard-smoke: image
	python3 tests/dashboard_smoke.py --image "$(IMAGE)"

docker-review-smoke: image
	python3 tests/review_smoke.py --image "$(IMAGE)"

ci:
	bash scripts/ci.sh

package:
	GO="$(GO)" VERSION="$(VERSION)" BUILD_GOARCH="$(BUILD_GOARCH)" bash scripts/package.sh

image docker-build:
	docker build --build-arg VERSION="$(VERSION)" -t "$(IMAGE)" .

docker-smoke: image
	bash tests/smoke.sh --image "$(IMAGE)"

clean:
	rm -rf bin .builds
