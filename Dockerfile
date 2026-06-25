FROM golang:1.25-bookworm AS builder

WORKDIR /build

# No C toolchain needed: modernc.org/sqlite is pure Go, so the build is
# CGO-free and cross-compiles natively. git is kept for build metadata.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
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

# CGO-free static build. modernc.org/sqlite bundles FTS5 by default, so no
# build tag is required. The static binary runs on a distroless base.
RUN test -n "${TARGETARCH}" || (echo "TARGETARCH build argument must be set" >&2; exit 1) && \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath \
      -ldflags="-s -w \
        -X github.com/nugget/thane-ai-agent/internal/platform/buildinfo.Version=${THANE_VERSION} \
        -X github.com/nugget/thane-ai-agent/internal/platform/buildinfo.GitCommit=${BUILD_COMMIT} \
        -X github.com/nugget/thane-ai-agent/internal/platform/buildinfo.GitBranch=${BUILD_BRANCH} \
        -X github.com/nugget/thane-ai-agent/internal/platform/buildinfo.BuildTime=${BUILD_TIME}" \
      -o /out/thane ./cmd/thane

# Pre-create runtime data dirs owned by the distroless nonroot uid (65532),
# since the distroless final stage has no shell to mkdir/chown.
RUN mkdir -p /out/data /out/config && chown -R 65532:65532 /out/data /out/config

FROM scratch AS artifact

COPY --from=builder /out/thane /thane

FROM gcr.io/distroless/static-debian12:nonroot

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
    org.opencontainers.image.base.name="gcr.io/distroless/static-debian12" \
    io.hass.name="Thane" \
    io.hass.description="Autonomous AI agent for Home Assistant" \
    io.hass.version="${THANE_VERSION}" \
    io.hass.type="addon" \
    io.hass.arch="aarch64|amd64"

# Distroless static base ships ca-certificates, tzdata, and a nonroot user
# (uid 65532) — no apt, shell, or libc. The CGO-free binary needs nothing
# more. Runtime data dirs were pre-created with nonroot ownership in the
# builder (the final stage has no shell to mkdir/chown).
COPY --from=builder /out/thane /usr/local/bin/thane
COPY --from=builder --chown=65532:65532 /out/data /data
COPY --from=builder --chown=65532:65532 /out/config /config

WORKDIR /data

USER nonroot

# Default ports
EXPOSE 8080
EXPOSE 11434

# Health check. Distroless has no shell or wget, so the binary probes its
# own /health endpoint via the `health` subcommand.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD ["/usr/local/bin/thane", "health"]

ENTRYPOINT ["/usr/local/bin/thane"]
CMD ["serve"]
