pkg := "github.com/nugget/thane-ai-agent/internal/buildinfo"
version := `git describe --tags --always --dirty 2>/dev/null || echo "dev"`
git_commit := `git rev-parse --short HEAD 2>/dev/null || echo "unknown"`
git_branch := `git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown"`
build_time := `date -u '+%Y-%m-%dT%H:%M:%SZ'`

ldflags := "-X " + pkg + ".Version=" + version + " -X " + pkg + ".GitCommit=" + git_commit + " -X " + pkg + ".GitBranch=" + git_branch + " -X " + pkg + ".BuildTime=" + build_time

host_os := if os() == "macos" { "darwin" } else { os() }
host_arch := if arch() == "aarch64" { "arm64" } else if arch() == "x86_64" { "amd64" } else { arch() }
thane-home := home_directory() / "Thane"
install-prefix := if os() == "macos" { env("INSTALL_PREFIX", thane-home) } else { env("INSTALL_PREFIX", "/usr/local") }

# List available recipes
default:
    @just --list

# --- Build ---

# Build a binary into dist/ (defaults to current platform, or specify OS/ARCH)
[group('build')]
build target_os=host_os target_arch=host_arch:
    @mkdir -p dist
    GOOS={{target_os}} GOARCH={{target_arch}} go build -ldflags "{{ldflags}}" -o dist/thane-{{target_os}}-{{target_arch}} ./cmd/thane
    @echo "Built dist/thane-{{target_os}}-{{target_arch}}"

# Build for all release targets
[group('build')]
build-all:
    just build linux amd64
    just build linux arm64
    just build darwin amd64
    just build darwin arm64

# Build and show version
[group('build')]
version: build
    dist/thane-{{host_os}}-{{host_arch}} version

# Clean build artifacts
[group('build')]
clean:
    rm -rf dist

# --- Test ---

# Run tests (always with race detector)
[group('test')]
test:
    go test -race ./...

# Check formatting
[group('test')]
fmt-check:
    @test -z "$(gofmt -l .)" || (echo "Files need formatting:" && gofmt -l . && exit 1)

# Run linter
[group('test')]
lint:
    golangci-lint run ./...

# CI: format check, lint, and tests
[group('test')]
ci: fmt-check lint test

# --- Install ---

# Install the binary to the appropriate system location
[group('deploy')]
[linux]
install: build
    install -D dist/thane-{{host_os}}-{{host_arch}} {{install-prefix}}/bin/thane

# Install the binary to the appropriate system location
[group('deploy')]
[macos]
install: build
    @mkdir -p {{install-prefix}}/bin
    install dist/thane-{{host_os}}-{{host_arch}} {{install-prefix}}/bin/thane

# Uninstall the binary
[group('deploy')]
uninstall:
    rm -f {{install-prefix}}/bin/thane

# --- Service ---

# Install and enable the system service (systemd unit + user + directories)
[group('deploy')]
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
    echo "  1. Copy your config:  cp examples/config.example.yaml /etc/thane/config.yaml"
    echo "  2. Edit secrets:      $EDITOR /etc/thane/config.yaml"
    echo "  3. Lock it down:      chmod 600 /etc/thane/config.yaml && chown thane:thane /etc/thane/config.yaml"
    echo "  4. Start it up:       systemctl start thane"

# Install and enable the system service (launchd user agent)
[group('deploy')]
[macos]
service-install: install
    #!/usr/bin/env sh
    set -e
    THANE_HOME="{{thane-home}}"
    # Create directory structure
    mkdir -p "$THANE_HOME/db"
    # Generate plist with absolute paths for this user
    mkdir -p ~/Library/LaunchAgents
    sed -e "s|/usr/local/bin/thane|$THANE_HOME/bin/thane|g" \
        -e "s|/usr/local/var/thane|$THANE_HOME|g" \
        init/info.nugget.thane.plist > ~/Library/LaunchAgents/info.nugget.thane.plist
    echo ""
    echo "Service installed as user launch agent."
    echo "  Home:    $THANE_HOME/"
    echo "  Binary:  $THANE_HOME/bin/thane"
    echo "  Config:  $THANE_HOME/config.yaml"
    echo "  Data:    $THANE_HOME/db/"
    echo "  Logs:    $THANE_HOME/thane.log"
    echo ""
    echo "Next steps:"
    echo "  1. Copy your config:  cp examples/config.example.yaml $THANE_HOME/config.yaml"
    echo "  2. Edit secrets:      \$EDITOR $THANE_HOME/config.yaml"
    echo "  3. Lock it down:      chmod 600 $THANE_HOME/config.yaml"
    echo "  4. Start it up:       launchctl load ~/Library/LaunchAgents/info.nugget.thane.plist"

# Remove the system service
[group('deploy')]
[linux]
service-uninstall:
    #!/usr/bin/env sh
    set -e
    systemctl stop thane.service 2>/dev/null || true
    systemctl disable thane.service 2>/dev/null || true
    rm -f /etc/systemd/system/thane.service
    systemctl daemon-reload
    echo "Service removed. To fully clean up:"
    echo "  rm -rf /var/lib/thane /etc/thane"
    echo "  userdel thane"

# Remove the system service
[group('deploy')]
[macos]
service-uninstall:
    #!/usr/bin/env sh
    set -e
    launchctl unload ~/Library/LaunchAgents/info.nugget.thane.plist 2>/dev/null || true
    rm -f ~/Library/LaunchAgents/info.nugget.thane.plist
    echo "Service removed. To fully clean up:"
    echo "  rm -rf {{thane-home}}"

# Show service status
[group('operations')]
[linux]
service-status:
    systemctl status thane.service

# Show service status
[group('operations')]
[macos]
service-status:
    @launchctl list info.nugget.thane 2>/dev/null || echo "Service not loaded"

# Bootstrap a working directory from repo sources (config, talents, persona)
[group('deploy')]
init dir="Thane":
    #!/usr/bin/env sh
    set -e
    mkdir -p "{{dir}}/db" "{{dir}}/talents"
    if [ ! -f "{{dir}}/config.yaml" ]; then
        cp examples/config.example.yaml "{{dir}}/config.yaml"
        echo "Created {{dir}}/config.yaml — edit with your settings"
    else
        echo "{{dir}}/config.yaml already exists, skipping"
    fi
    for f in talents/*.md; do
        [ -f "$f" ] && cp -n "$f" "{{dir}}/talents/" 2>/dev/null || true
    done
    echo "Copied talents to {{dir}}/talents/"
    if [ ! -f "{{dir}}/persona.md" ]; then
        if [ -f persona.md ]; then
            cp persona.md "{{dir}}/persona.md"
        elif [ -f examples/persona.example.md ]; then
            cp examples/persona.example.md "{{dir}}/persona.md"
        fi
        echo "Created {{dir}}/persona.md — customize your agent's personality"
    else
        echo "{{dir}}/persona.md already exists, skipping"
    fi
    echo ""
    echo "Working directory ready: {{dir}}/"
    echo "  1. Edit config:  $EDITOR {{dir}}/config.yaml"
    echo "  2. Run:          just serve"

# Build and run from the local Thane/ working directory (for development)
[group('operations')]
serve: build
    cd Thane && ../dist/thane-{{host_os}}-{{host_arch}} serve

# Tail live service logs (default: dev workdir)
[group('operations')]
logs workdir="./Thane":
    tail -f {{workdir}}/thane.log

# --- Release ---

# Tag and publish a GitHub release (usage: just release 0.2.0)
[group('release-engineering')]
release tag:
    git tag -a "v{{tag}}" -m "Release v{{tag}}"
    git push origin "v{{tag}}"
    gh release create "v{{tag}}" --generate-notes --title "v{{tag}}"
