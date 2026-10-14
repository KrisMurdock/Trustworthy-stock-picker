VERSION ?= $(shell git rev-parse HEAD)
GO ?= go

.PHONY: test vet build image clean

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o bin/stock-watcher ./cmd/stock-watcher

image:
	docker build --build-arg VERSION=$(VERSION) -t stock-watcher:$(VERSION) .

clean:
	rm -rf bin
