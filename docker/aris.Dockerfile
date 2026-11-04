FROM golang:1.26.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/aris-stock ./cmd/aris-stock
COPY internal/maexec ./internal/maexec
COPY internal/aris ./internal/aris
COPY internal/logging ./internal/logging
ARG VERSION=development
RUN CGO_ENABLED=0 go build -p=1 -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /aris-stock ./cmd/aris-stock

FROM alpine:3.22
ARG VERSION=development
LABEL org.opencontainers.image.title="aris-stock" \
      org.opencontainers.image.revision="${VERSION}"
RUN apk add --no-cache ca-certificates && addgroup -g 10001 aris && adduser -D -H -u 10001 -G aris aris && mkdir -p /state /logs && chown aris:aris /state /logs
COPY --from=build /aris-stock /usr/local/bin/aris-stock
USER 10001:10001
WORKDIR /state
ENV STOCK_WATCHER_LOG_DIR=/logs STOCK_WATCHER_LOG_COLOR=off
ENTRYPOINT ["/usr/local/bin/aris-stock"]
CMD ["version"]
