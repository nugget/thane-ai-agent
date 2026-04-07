pkg := "github.com/nugget/thane-ai-agent/internal/buildinfo"
version := env("THANE_VERSION", `git describe --tags --always --dirty 2>/dev/null || echo "dev"`)
git_commit := `git rev-parse --short HEAD 2>/dev/null || echo "unknown"`
git_branch := `git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown"`
build_time := `date -u '+%Y-%m-%dT%H:%M:%SZ'`
changelog := `git log --oneline $(git describe --tags --abbrev=0 2>/dev/null || echo HEAD)..HEAD --no-merges 2>/dev/null | head -5 | sed 's/^[a-f0-9]* //' | tr "'" "_" | tr '\n' ';' | sed 's/;$//' | sed 's/;/; /g' || echo ""`

ldflags := "-X " + pkg + ".Version=" + version + " -X " + pkg + ".GitCommit=" + git_commit + " -X " + pkg + ".GitBranch=" + git_branch + " -X " + pkg + ".BuildTime=" + build_time + " -X '" + pkg + ".Changelog=" + changelog + "'"

host_os := if os() == "macos" { "darwin" } else { os() }
host_arch := if arch() == "aarch64" { "arm64" } else if arch() == "x86_64" { "amd64" } else { arch() }
thane-home := home_directory() / "Thane"
install-prefix := if os() == "macos" { env("INSTALL_PREFIX", thane-home) } else { env("INSTALL_PREFIX", "/usr/local") }
release-dir := "dist/release"
codesign-identity := env("THANE_CODESIGN_IDENTITY", "-")
codesign-options := env("THANE_CODESIGN_OPTIONS", "runtime")
codesign-timestamp := env("THANE_CODESIGN_TIMESTAMP", "true")
notary-profile := env("THANE_NOTARY_PROFILE", "")

# List available recipes
default:
    @just --list

# --- Build ---

# Copy default files into embeddable positions for go:embed, and
# regenerate examples/config.example.yaml from internal/config.
[group('build')]
generate:
    go generate ./internal/talents/ ./cmd/thane/
    go generate ./internal/config/...

# Build a binary into dist/ (defaults to current platform, or specify OS/ARCH)
[group('build')]
build target_os=host_os target_arch=host_arch cc="": generate
    @mkdir -p dist
    @if [ -n "{{cc}}" ]; then export CC="{{cc}}"; fi; \
    CGO_ENABLED=1 GOOS={{target_os}} GOARCH={{target_arch}} go build -trimpath -tags "sqlite_fts5" -ldflags "{{ldflags}}" -o dist/thane-{{target_os}}-{{target_arch}} ./cmd/thane
    @# Ad-hoc sign macOS binaries so Gatekeeper doesn't kill them on each rebuild
    @if [ "{{target_os}}" = "darwin" ] && [ "{{host_os}}" = "darwin" ]; then codesign -s - dist/thane-{{target_os}}-{{target_arch}} 2>/dev/null && echo "Signed dist/thane-{{target_os}}-{{target_arch}}"; fi
    @echo "Built dist/thane-{{target_os}}-{{target_arch}}"

# Build for all release targets
[group('build')]
build-all:
    just build linux amd64
    just build linux arm64
    just build darwin amd64
    just build darwin arm64

# Build a local container image for the current checkout
[group('build')]
container tag="thane:dev":
    docker build \
        --build-arg THANE_VERSION="{{version}}" \
        --build-arg BUILD_COMMIT="{{git_commit}}" \
        --build-arg BUILD_BRANCH="{{git_branch}}" \
        --build-arg BUILD_TIME="{{build_time}}" \
        -t {{tag}} .

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
test: generate
    go test -race ./...

# Check formatting
[group('test')]
fmt-check:
    @test -z "$(gofmt -l .)" || (echo "Files need formatting:" && gofmt -l . && exit 1)

# Run linter
[group('test')]
lint: generate
    golangci-lint run ./...

# Check go.mod/go.sum are tidy
[group('test')]
mod-tidy-check:
    go mod tidy
    @test -z "$(git diff --name-only go.mod go.sum)" || (echo "go.mod/go.sum not tidy — run 'go mod tidy'" && git diff go.mod go.sum && exit 1)

# Check examples/config.example.yaml is up to date with the config package.
# Runs go generate and fails if the file changed (i.e., was stale).
[group('test')]
config-generate-check:
    go generate ./internal/config/...
    @test -z "$(git diff --name-only examples/config.example.yaml)" || (echo "examples/config.example.yaml is stale — run 'go generate ./internal/config/...' and commit the result" && git diff examples/config.example.yaml && exit 1)

# Architecture metrics report — shows package count, interface count, large files, Set* mutators, and database.Open call sites vs baselines.
[group('test')]
architecture:
    @bash scripts/architecture.sh report

# Architecture guardrail check — fails if any metric in scripts/architecture.baseline is exceeded.
# Run 'scripts/architecture.sh update' to advance a baseline when the growth is intentional.
[group('test')]
architecture-check:
    @bash scripts/architecture.sh check

# Check internal markdown links (no network requests)
[group('test')]
link-check:
    lychee --offline --no-progress '**/*.md'

# CI: format check, lint, and tests
[group('test')]
ci: fmt-check mod-tidy-check config-generate-check architecture-check link-check lint test

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
    mkdir -p "$THANE_HOME/logs"
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
    echo "  Logs:    $THANE_HOME/logs/thane.log  (rotated daily)"
    echo "  Crashes: $THANE_HOME/crash.log       (pre-init errors only)"
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

# Build from main branch and deploy to ~/Thane (ensures clean release metadata)
[group('deploy')]
[macos]
deploy:
    #!/usr/bin/env sh
    set -e
    current_branch=$(git rev-parse --abbrev-ref HEAD)
    if [ "$current_branch" != "main" ]; then
        echo "Switching to main (was on $current_branch)..."
        git checkout main
        git pull origin main
    fi
    git fetch --tags
    just build
    cp dist/thane-{{host_os}}-{{host_arch}} {{thane-home}}/bin/thane
    echo "Deployed $(dist/thane-{{host_os}}-{{host_arch}} version 2>/dev/null || echo 'thane') to {{thane-home}}/bin/thane"
    launchctl kickstart -k gui/$(id -u)/info.nugget.thane
    echo "Service restarted."
    if [ "$current_branch" != "main" ]; then
        echo "Returning to $current_branch..."
        git checkout "$current_branch"
    fi

# Build, optionally sign/notarize, and atomically copy a binary to a remote host
[group('deploy')]
deploy-scp host remote_bin="Thane/bin/thane" target_os=host_os target_arch=host_arch cc="" restart_cmd="":
    #!/usr/bin/env bash
    set -euo pipefail
    host="{{host}}"
    remote_bin="{{remote_bin}}"
    target_os="{{target_os}}"
    target_arch="{{target_arch}}"
    cc="{{cc}}"
    restart_cmd="{{restart_cmd}}"
    binary="dist/thane-${target_os}-${target_arch}"
    version="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"

    test -n "$host" || { echo "host is required"; exit 1; }

    if [ -n "$cc" ]; then
        THANE_VERSION="$version" just build "$target_os" "$target_arch" "$cc"
    else
        THANE_VERSION="$version" just build "$target_os" "$target_arch"
    fi

    if [ "$target_os" = "darwin" ] && [ "{{host_os}}" = "darwin" ]; then
        just macos-sign "$binary"
        if [ -n "${THANE_NOTARY_PROFILE:-}" ]; then
            if [ -n "$cc" ]; then
                archive="$(just --quiet release-archive "$version" "$target_os" "$target_arch" "$cc" | tail -n 1)"
            else
                archive="$(just --quiet release-archive "$version" "$target_os" "$target_arch" | tail -n 1)"
            fi
            echo "Notarized archive ready: $archive"
        fi
    fi

    temp_bin="${remote_bin}.new"
    scp -p "$binary" "${host}:${temp_bin}"
    ssh "$host" "mv '$temp_bin' '$remote_bin'"
    if [ -n "$restart_cmd" ]; then
        ssh "$host" "$restart_cmd"
    fi
    echo "Deployed $binary to ${host}:${remote_bin}"

# Build and run from the local Thane/ working directory (for development)
[group('operations')]
serve: build
    cd Thane && ../dist/thane-{{host_os}}-{{host_arch}} serve

# Tail live service logs (default: dev workdir)
[group('operations')]
logs workdir="./Thane":
    tail -f {{workdir}}/logs/thane.log

# Live smoke test for loops-ng loop definition registry behavior against a running dev instance
[group('operations')]
loop-definition-smoke base_url="http://127.0.0.1:8080":
    python3 -u scripts/loop_definition_smoke.py --base-url {{base_url}}

# Focused loops-ng regression pass for the packages that own the new
# loop definition, launch, completion, app delivery, and interactive
# channel integration surfaces.
[group('operations')]
web-static-check:
    node --check internal/server/web/static/app.js
    node --check internal/server/web/static/detail.js
    node --check internal/server/web/static/request.js
    node --check internal/server/web/static/shared.js

# Focused loops-ng regression pass for the packages that own the new
# loop definition, launch, completion, app delivery, interactive
# channel integration, and dashboard graph surfaces.
[group('operations')]
loops-ng-contract-tests:
    just web-static-check
    go test -race ./internal/loop ./internal/tools ./internal/delegate ./internal/app ./internal/channels/signal ./internal/server/api

# Broader loops-ng smoke pass: focused regression packages plus live
# loop-definition runtime smoke against a running dev instance.
[group('operations')]
loops-ng-smoke base_url="http://127.0.0.1:8080":
    just loops-ng-contract-tests
    just loop-definition-smoke {{base_url}}

# Live smoke test with restart/persistence validation. Example:
# RESTART_CMD='cd /path/to/dev-workspace && just restart' just loop-definition-persistence
[group('operations')]
loop-definition-persistence base_url="http://127.0.0.1:8080":
    @test -n "$RESTART_CMD" || (echo "Set RESTART_CMD to the restart command for your live dev instance" && exit 1)
    RESTART_CMD="$RESTART_CMD" python3 -u scripts/loop_definition_smoke.py --base-url {{base_url}} --restart-cmd "$RESTART_CMD"

# Full loops-ng persistence pass: focused regression packages plus the
# live restart/persistence harness.
[group('operations')]
loops-ng-persistence base_url="http://127.0.0.1:8080":
    @test -n "$RESTART_CMD" || (echo "Set RESTART_CMD to the restart command for your live dev instance" && exit 1)
    just loops-ng-contract-tests
    RESTART_CMD="$RESTART_CMD" just loop-definition-persistence {{base_url}}

# --- Release ---

# Sign a macOS binary with either Developer ID or ad-hoc identity
[group('release-engineering')]
macos-sign binary identity=codesign-identity options=codesign-options timestamp=codesign-timestamp:
    #!/usr/bin/env bash
    set -euo pipefail
    test "{{host_os}}" = "darwin" || { echo "macos-sign requires a macOS host"; exit 1; }

    binary="{{binary}}"
    identity="{{identity}}"
    options="{{options}}"
    timestamp="{{timestamp}}"

    args=(codesign --force --sign "$identity")
    if [ "$identity" != "-" ] && [ -n "$options" ]; then
        args+=(--options "$options")
    fi
    if [ "$identity" != "-" ] && [ "$timestamp" = "true" ]; then
        args+=(--timestamp)
    fi
    args+=("$binary")

    "${args[@]}"
    codesign --verify --verbose=2 "$binary"
    codesign -dv --verbose=4 "$binary"

# Submit a packaged macOS archive to Apple's notary service
[group('release-engineering')]
[macos]
macos-notarize archive profile=notary-profile:
    test "{{codesign-identity}}" != "-" || (echo "Notarization requires THANE_CODESIGN_IDENTITY to name a Developer ID Application certificate" && exit 1)
    test -n "{{profile}}" || (echo "Set THANE_NOTARY_PROFILE or pass a notary profile name" && exit 1)
    xcrun notarytool submit "{{archive}}" --keychain-profile "{{profile}}" --wait

# Build and package a release archive for a single target
[group('release-engineering')]
release-archive version target_os=host_os target_arch=host_arch cc="":
    #!/usr/bin/env bash
    set -euo pipefail
    version="{{version}}"
    target_os="{{target_os}}"
    target_arch="{{target_arch}}"
    cc="{{cc}}"
    binary="dist/thane-${target_os}-${target_arch}"

    version="${version#v}"
    if [ -n "$cc" ]; then
        THANE_VERSION="v${version}" just build "$target_os" "$target_arch" "$cc"
    else
        THANE_VERSION="v${version}" just build "$target_os" "$target_arch"
    fi

    if [ "$target_os" = "darwin" ] && [ "{{host_os}}" = "darwin" ]; then
        just macos-sign "$binary"
    fi

    archive="$(scripts/package-release.sh "$version" "$target_os" "$target_arch" "$binary" "{{release-dir}}")"

    if [ "$target_os" = "darwin" ] && [ "{{host_os}}" = "darwin" ] && [ -n "${THANE_NOTARY_PROFILE:-}" ]; then
        just macos-notarize "$archive"
    fi

    printf '%s\n' "$archive"

# Generate SHA-256 checksums for packaged release archives
[group('release-engineering')]
release-checksums version:
    #!/usr/bin/env bash
    set -euo pipefail
    version="{{version}}"
    version="${version#v}"
    output="{{release-dir}}/thane_${version}_checksums.txt"

    cd "{{release-dir}}"
    shopt -s nullglob
    archives=(./*.tar.gz ./*.zip)
    if [ "${#archives[@]}" -eq 0 ]; then
        echo "No release archives found in {{release-dir}}" >&2
        exit 1
    fi

    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "${archives[@]}" > "$(basename "$output")"
    else
        shasum -a 256 "${archives[@]}" > "$(basename "$output")"
    fi

    printf '%s\n' "$output"

# Build a local release snapshot for the current target and emit checksums
[group('release-engineering')]
release-snapshot version target_os=host_os target_arch=host_arch cc="":
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -n "{{cc}}" ]; then
        just release-archive "{{version}}" "{{target_os}}" "{{target_arch}}" "{{cc}}"
    else
        just release-archive "{{version}}" "{{target_os}}" "{{target_arch}}"
    fi
    just release-checksums "{{version}}"

# Tag main for release and let GitHub Actions publish assets and containers
[group('release-engineering')]
release version:
    #!/usr/bin/env bash
    set -euo pipefail
    version="{{version}}"
    version="${version#v}"
    tag="v${version}"

    if ! printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'; then
        echo "Version must look like 0.9.0 or 0.9.0-rc.1" >&2
        exit 1
    fi

    test -z "$(git status --short)" || { echo "Worktree must be clean before cutting a release"; exit 1; }
    test "$(git rev-parse --abbrev-ref HEAD)" = "main" || { echo "Release tags must be cut from main"; exit 1; }

    git fetch origin main --tags
    test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" || { echo "Local main must match origin/main before release"; exit 1; }
    ! git rev-parse "$tag" >/dev/null 2>&1 || { echo "Tag already exists: $tag"; exit 1; }

    just ci
    git tag -a "$tag" -m "Release $tag"
    git push origin "$tag"
    echo "Pushed $tag. GitHub Actions will build release archives, attach them to the GitHub release, and publish the container image."
