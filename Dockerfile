# syntax=docker/dockerfile:1
# Static Go binary (no cgo) on a small Debian base; nothing is fetched at
# runtime.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/mcp-piratebay ./cmd/mcp-piratebay

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tini \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --no-create-home --home-dir /nonexistent app \
    && mkdir -p /data && chown 10001:10001 /data
COPY --from=build /out/mcp-piratebay /usr/local/bin/mcp-piratebay
COPY LICENSE UPSTREAM.md /usr/share/doc/openplayx-torrent-search/
USER 10001
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8790
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD ["/usr/local/bin/mcp-piratebay", "healthcheck", "http://127.0.0.1:8790/healthz"]
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/mcp-piratebay", "--config", "/config/config.json", "--secrets", "/run/secrets/torrent-search.json"]
