FROM golang:1.26.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY runtime ./runtime
ARG VERSION=development
RUN CGO_ENABLED=0 go build -p=1 -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /stock-watcher ./cmd/stock-watcher

FROM alpine:3.22
ARG VERSION=development
LABEL org.opencontainers.image.title="stock-watcher" \
      org.opencontainers.image.revision="${VERSION}"
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /stock-watcher /usr/local/bin/stock-watcher
COPY leaders.yaml /app/leaders.yaml
ENV STOCK_WATCHER_DATA_DIR=/data STOCK_WATCHER_LOG_DIR=/logs STOCK_WATCHER_LOG_COLOR=off STOCK_WATCHER_WEB_ADDR=0.0.0.0:8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/stock-watcher"]
CMD ["run"]
