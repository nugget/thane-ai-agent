FROM golang:1.25-bookworm AS builder

WORKDIR /build

# Install build dependencies (including CGO for SQLite)
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        gcc \
        git \
        libc6-dev \
    && rm -rf /var/lib/apt/lists/*

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
RUN test -n "${TARGETARCH}" || (echo "TARGETARCH build argument must be set" >&2; exit 1) && \
    CGO_ENABLED=1 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -tags "sqlite_fts5" \
      -ldflags="-s -w \
        -X github.com/nugget/thane-ai-agent/internal/platform/buildinfo.Version=${THANE_VERSION} \
        -X github.com/nugget/thane-ai-agent/internal/platform/buildinfo.GitCommit=${BUILD_COMMIT} \
        -X github.com/nugget/thane-ai-agent/internal/platform/buildinfo.GitBranch=${BUILD_BRANCH} \
        -X github.com/nugget/thane-ai-agent/internal/platform/buildinfo.BuildTime=${BUILD_TIME}" \
      -o /out/thane ./cmd/thane

FROM scratch AS artifact

COPY --from=builder /out/thane /thane

FROM debian:bookworm-slim

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
    org.opencontainers.image.base.name="docker.io/library/debian:bookworm-slim" \
    io.hass.name="Thane" \
    io.hass.description="Autonomous AI agent for Home Assistant" \
    io.hass.version="${THANE_VERSION}" \
    io.hass.type="addon" \
    io.hass.arch="aarch64|amd64"

# Install runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        tzdata \
        wget \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user
RUN useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin thane

# Copy binary from builder
COPY --from=builder /out/thane /usr/local/bin/thane

# Create data directories
RUN mkdir -p /data /config && chown -R thane:thane /data /config

WORKDIR /data

USER thane

# Default ports
EXPOSE 8080
EXPOSE 11434

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

ENTRYPOINT ["/usr/local/bin/thane"]
CMD ["serve"]
