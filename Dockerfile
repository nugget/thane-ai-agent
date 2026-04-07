FROM golang:1.25-alpine AS builder

WORKDIR /build

# Install build dependencies (including CGO for SQLite)
RUN apk add --no-cache git ca-certificates gcc musl-dev

# Copy go mod files first for layer caching
COPY go.mod go.sum* ./
RUN go mod download

# Copy source
COPY . .

ARG TARGETOS=linux
ARG TARGETARCH
ARG THANE_VERSION=dev
ARG BUILD_COMMIT=unknown
ARG BUILD_BRANCH=unknown
ARG BUILD_TIME=unknown

# Build with CGO for SQLite support
RUN CGO_ENABLED=1 GOOS="${TARGETOS}" GOARCH="${TARGETARCH:-amd64}" \
    go build -trimpath -tags "sqlite_fts5" \
      -ldflags="-s -w \
        -X github.com/nugget/thane-ai-agent/internal/buildinfo.Version=${THANE_VERSION} \
        -X github.com/nugget/thane-ai-agent/internal/buildinfo.GitCommit=${BUILD_COMMIT} \
        -X github.com/nugget/thane-ai-agent/internal/buildinfo.GitBranch=${BUILD_BRANCH} \
        -X github.com/nugget/thane-ai-agent/internal/buildinfo.BuildTime=${BUILD_TIME}" \
      -o /out/thane ./cmd/thane

FROM scratch AS artifact

COPY --from=builder /out/thane /thane

FROM alpine:3.20

ARG THANE_VERSION=dev
ARG BUILD_COMMIT=unknown
ARG BUILD_TIME=unknown

LABEL \
    org.opencontainers.image.title="Thane" \
    org.opencontainers.image.description="Autonomous AI agent for Home Assistant" \
    org.opencontainers.image.authors="David McNett (https://github.com/nugget)" \
    org.opencontainers.image.url="https://github.com/nugget/thane-ai-agent" \
    org.opencontainers.image.source="https://github.com/nugget/thane-ai-agent" \
    org.opencontainers.image.documentation="https://github.com/nugget/thane-ai-agent/tree/main/docs" \
    org.opencontainers.image.vendor="nugget" \
    org.opencontainers.image.licenses="Apache-2.0" \
    org.opencontainers.image.version="${THANE_VERSION}" \
    org.opencontainers.image.ref.name="${THANE_VERSION}" \
    org.opencontainers.image.revision="${BUILD_COMMIT}" \
    org.opencontainers.image.created="${BUILD_TIME}" \
    org.opencontainers.image.base.name="docker.io/library/alpine:3.20" \
    io.hass.name="Thane" \
    io.hass.description="Autonomous AI agent for Home Assistant" \
    io.hass.version="${THANE_VERSION}" \
    io.hass.type="addon" \
    io.hass.arch="aarch64|amd64"

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN adduser -D -H -s /sbin/nologin thane

# Copy binary from builder
COPY --from=builder /out/thane /usr/local/bin/thane

# Create data directories
RUN mkdir -p /data /config && chown -R thane:thane /data /config

USER thane

# Default ports
EXPOSE 8080
EXPOSE 11434

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

ENTRYPOINT ["/usr/local/bin/thane"]
CMD ["serve"]
