# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Install build dependencies (including CGO for SQLite)
RUN apk add --no-cache git ca-certificates gcc musl-dev

# Copy go mod files first for layer caching
COPY go.mod go.sum* ./
RUN go mod download || true

# Copy source
COPY . .

# Build with CGO for SQLite support
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-w -s" -o thane ./cmd/thane

# Runtime stage
FROM alpine:3.20

# Labels for Home Assistant Add-on
LABEL \
    io.hass.name="Thane" \
    io.hass.description="Autonomous AI Agent for Home Assistant" \
    io.hass.version="0.1.0" \
    io.hass.type="addon" \
    io.hass.arch="aarch64|amd64|armv7"

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN adduser -D -H -s /sbin/nologin thane

# Copy binary from builder
COPY --from=builder /build/thane /usr/local/bin/thane

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
