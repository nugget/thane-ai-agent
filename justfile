pkg := "github.com/nugget/thane-ai-agent/internal/buildinfo"
version := `git describe --tags --always --dirty 2>/dev/null || echo "dev"`
git_commit := `git rev-parse --short HEAD 2>/dev/null || echo "unknown"`
git_branch := `git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown"`
build_time := `date -u '+%Y-%m-%dT%H:%M:%SZ'`

ldflags := "-X " + pkg + ".Version=" + version + " -X " + pkg + ".GitCommit=" + git_commit + " -X " + pkg + ".GitBranch=" + git_branch + " -X " + pkg + ".BuildTime=" + build_time

host_os := if os() == "macos" { "darwin" } else { os() }
host_arch := if arch() == "aarch64" { "arm64" } else if arch() == "x86_64" { "amd64" } else { arch() }
install-prefix := env("INSTALL_PREFIX", "/usr/local")

# List available recipes
default:
    @just --list

# Build a binary into dist/ (defaults to current platform, or specify OS/ARCH)
build target_os=host_os target_arch=host_arch:
    @mkdir -p dist
    GOOS={{target_os}} GOARCH={{target_arch}} go build -ldflags "{{ldflags}}" -o dist/thane-{{target_os}}-{{target_arch}} ./cmd/thane
    @echo "Built dist/thane-{{target_os}}-{{target_arch}}"

# Build for all release targets
build-all:
    just build linux amd64
    just build linux arm64
    just build darwin amd64
    just build darwin arm64

# Run tests
test:
    go test ./...

# Run tests with race detector
test-race:
    go test -race ./...

# Check formatting
fmt-check:
    @test -z "$(gofmt -l .)" || (echo "Files need formatting:" && gofmt -l . && exit 1)

# Run linter (if golangci-lint is available)
lint:
    golangci-lint run ./... || true

# CI: format check, lint, tests with race detector
ci: fmt-check lint test-race

# Build and show version
version: build
    dist/thane-{{host_os}}-{{host_arch}} version

# Install the binary to the appropriate system location
[linux]
install: build
    install -D dist/thane-{{host_os}}-{{host_arch}} {{install-prefix}}/bin/thane

[macos]
install: build
    install dist/thane-{{host_os}}-{{host_arch}} {{install-prefix}}/bin/thane

# Uninstall the binary
uninstall:
    rm -f {{install-prefix}}/bin/thane

# Install systemd service, create user and directories
[linux]
service-install: install
    #!/usr/bin/env sh
    set -e
    # Create service user (no login, no home dir creation — StateDirectory handles it)
    if ! id thane >/dev/null 2>&1; then
        useradd --system --no-create-home --shell /usr/sbin/nologin thane
        echo "Created system user: thane"
    fi
    # Install unit file
    install -m 644 init/thane.service /etc/systemd/system/thane.service
    # Ensure state and config directories exist with proper ownership
    install -d -o thane -g thane -m 750 /var/lib/thane
    install -d -o thane -g thane -m 750 /etc/thane
    # Reload and enable
    systemctl daemon-reload
    systemctl enable thane.service
    echo ""
    echo "Service installed and enabled."
    echo "  Config:  /etc/thane/config.yaml"
    echo "  State:   /var/lib/thane/"
    echo "  Logs:    journalctl -u thane"
    echo ""
    echo "Next steps:"
    echo "  1. Copy your config:  cp config.example.yaml /etc/thane/config.yaml"
    echo "  2. Edit secrets:      $EDITOR /etc/thane/config.yaml"
    echo "  3. Lock it down:      chmod 600 /etc/thane/config.yaml && chown thane:thane /etc/thane/config.yaml"
    echo "  4. Start it up:       systemctl start thane"

# Uninstall systemd service and clean up
[linux]
service-uninstall:
    #!/usr/bin/env sh
    set -e
    systemctl stop thane.service 2>/dev/null || true
    systemctl disable thane.service 2>/dev/null || true
    rm -f /etc/systemd/system/thane.service
    systemctl daemon-reload
    echo "Service removed. User 'thane' and /var/lib/thane preserved (remove manually if desired)."

# Install launchd service, create directories
[macos]
service-install: install
    #!/usr/bin/env sh
    set -e
    # Create state and config directories
    mkdir -p /usr/local/var/thane
    mkdir -p /usr/local/etc/thane
    # Install plist (user agent — runs as current user, no root required)
    cp init/info.nugget.thane.plist ~/Library/LaunchAgents/info.nugget.thane.plist
    echo ""
    echo "Service installed as user launch agent."
    echo "  Config:  /usr/local/etc/thane/config.yaml"
    echo "  State:   /usr/local/var/thane/"
    echo "  Logs:    log stream --process thane"
    echo ""
    echo "Next steps:"
    echo "  1. Copy your config:  cp config.example.yaml /usr/local/etc/thane/config.yaml"
    echo "  2. Edit secrets:      \$EDITOR /usr/local/etc/thane/config.yaml"
    echo "  3. Lock it down:      chmod 600 /usr/local/etc/thane/config.yaml"
    echo "  4. Start it up:       launchctl load ~/Library/LaunchAgents/info.nugget.thane.plist"

# Uninstall launchd service
[macos]
service-uninstall:
    #!/usr/bin/env sh
    set -e
    launchctl unload ~/Library/LaunchAgents/info.nugget.thane.plist 2>/dev/null || true
    rm -f ~/Library/LaunchAgents/info.nugget.thane.plist
    echo "Service removed. /usr/local/var/thane and logs preserved (remove manually if desired)."

# Show service status
[linux]
service-status:
    systemctl status thane.service

[macos]
service-status:
    launchctl list info.nugget.thane 2>/dev/null || echo "Service not loaded"

# Tail live logs
[linux]
logs:
    journalctl -u thane -f

[macos]
logs:
    log stream --process thane

# Clean build artifacts
clean:
    rm -rf dist

# Tag and publish a GitHub release (usage: just release 0.2.0)
release tag:
    git tag -a "v{{tag}}" -m "Release v{{tag}}"
    git push origin "v{{tag}}"
    gh release create "v{{tag}}" --generate-notes --title "v{{tag}}"
