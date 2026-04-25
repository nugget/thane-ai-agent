pkg := "github.com/nugget/thane-ai-agent/internal/platform/buildinfo"
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
pkg-dir := "dist/pkg"
codesign-identity := env("THANE_CODESIGN_IDENTITY", "-")
installer-identity := env("THANE_INSTALLER_IDENTITY", "")
codesign-options := env("THANE_CODESIGN_OPTIONS", "runtime")
codesign-timestamp := env("THANE_CODESIGN_TIMESTAMP", "true")
notary-profile := env("THANE_NOTARY_PROFILE", "")

# List available recipes
default:
    @echo "Common workflows:"
    @echo "  just release-github <version> [auto|prerelease|release]  # normal production release path"
    @echo "  just deploy-macos-pkg <user@host>                         # live-host pkg validation without a GitHub release"
    @echo "  just ci                                                   # full local validation gate"
    @echo ""
    @just --list

# --- Build ---

# Copy default files into embeddable positions for go:embed, and
# regenerate examples/config.example.yaml from internal/platform/config.
[group('build')]
generate:
    go generate ./internal/model/talents/ ./cmd/thane/
    go generate ./internal/platform/config/...

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
    just build darwin amd64
    just build darwin arm64
    just build-linux-docker amd64
    just build-linux-docker arm64

# Build a Linux binary through Docker Buildx so CGO-backed SQLite builds
# stay usable even on non-Linux hosts without local cross-compilers.
[group('build')]
build-linux-docker target_arch:
    #!/usr/bin/env bash
    set -euo pipefail
    target_arch="{{target_arch}}"
    export_dir="dist/docker-export/linux-${target_arch}"
    binary="dist/thane-linux-${target_arch}"

    rm -rf "$export_dir"
    mkdir -p "$export_dir" dist

    docker buildx build \
        --platform "linux/${target_arch}" \
        --target artifact \
        --build-arg THANE_VERSION="{{version}}" \
        --build-arg BUILD_COMMIT="{{git_commit}}" \
        --build-arg BUILD_BRANCH="{{git_branch}}" \
        --build-arg BUILD_TIME="{{build_time}}" \
        --output "type=local,dest=${export_dir}" \
        .

    install -m 755 "$export_dir/thane" "$binary"
    echo "Built $binary via Docker Buildx"

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
    go generate ./internal/platform/config/...
    @test -z "$(git diff --name-only examples/config.example.yaml)" || (echo "examples/config.example.yaml is stale — run 'go generate ./internal/platform/config/...' and commit the result" && git diff examples/config.example.yaml && exit 1)

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
    echo "  Logs:    $THANE_HOME/logs/{events,requests,access,loops,delegates,envelopes}/YYYY-MM-DD/HH.jsonl"
    echo "           $THANE_HOME/logs/logs.db"
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
        just release-sign-macos "$binary"
        if [ -n "${THANE_NOTARY_PROFILE:-}" ]; then
            if [ -n "$cc" ]; then
                archive="$(just release-build-archive "$version" "$target_os" "$target_arch" "$cc" | tail -n 1)"
            else
                archive="$(just release-build-archive "$version" "$target_os" "$target_arch" | tail -n 1)"
            fi
            test -n "$archive" || { echo "release-build-archive did not report an artifact path" >&2; exit 1; }
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

[doc("Operator path: build a signed macOS installer package from the current clean checkout")]
[group('release-engineering')]
[macos]
build-macos-pkg version="" target_arch=host_arch output_dir=pkg-dir:
    scripts/releng/build-macos-pkg.sh "{{version}}" "{{target_arch}}" "{{output_dir}}" true

[doc("Operator path: build, notarize, and deploy a macOS pkg to a remote Tahoe host, then verify the live API version")]
[group('deploy')]
[macos]
deploy-macos-pkg host target_arch=host_arch version="" remote_pkg_dir="/tmp/thane-releng" verify_url="http://127.0.0.1:8080/v1/version" verify_timeout_seconds="60":
    scripts/releng/deploy-macos-pkg.sh "{{host}}" "{{target_arch}}" "{{version}}" "{{remote_pkg_dir}}" "{{verify_url}}" "{{verify_timeout_seconds}}"

# Build and run from the local Thane/ working directory (for development)
[group('operations')]
serve: build
    cd Thane && ../dist/thane-{{host_os}}-{{host_arch}} serve

# Tail live service logs (default: dev workdir). Follows the events
# dataset and rolls to the next HH.jsonl segment automatically. Waits
# patiently if no segment exists yet so this works on a fresh install.
[group('operations')]
logs workdir="./Thane":
    #!/usr/bin/env bash
    set -euo pipefail
    events_dir="{{workdir}}/logs/events"
    echo "Tailing events dataset under $events_dir (Ctrl-C to stop)..."
    current=""
    while true; do
        latest="$(find "$events_dir" -type f -name '*.jsonl' 2>/dev/null | sort | tail -n 1 || true)"
        if [ -z "$latest" ]; then
            # No segment yet — wait for the first one. Repeat cheaply.
            sleep 2
            continue
        fi
        if [ "$latest" != "$current" ]; then
            if [ -n "$current" ]; then
                echo "--- rolled over to $latest ---"
            else
                echo "--- tailing $latest ---"
            fi
            current="$latest"
            # Stream the new segment in the foreground until the next
            # hour rolls. The outer loop kills tail when a newer file
            # appears (tail's --pid would be cleaner but isn't portable).
            tail -n 0 -F "$latest" &
            tail_pid=$!
            # Poll once a minute for a newer segment; kill tail when
            # one appears so the outer loop can latch on to it.
            while kill -0 "$tail_pid" 2>/dev/null; do
                sleep 60
                newer="$(find "$events_dir" -type f -name '*.jsonl' 2>/dev/null | sort | tail -n 1 || true)"
                if [ -n "$newer" ] && [ "$newer" != "$current" ]; then
                    kill "$tail_pid" 2>/dev/null || true
                    wait "$tail_pid" 2>/dev/null || true
                    break
                fi
            done
        fi
    done

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
    go test -race ./internal/runtime/loop ./internal/tools ./internal/runtime/delegate ./internal/app ./internal/channels/messaging/signal ./internal/server/api

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

[doc("Building block: sign a macOS binary")]
[group('release-engineering')]
release-sign-macos binary identity=codesign-identity options=codesign-options timestamp=codesign-timestamp:
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

[doc("Building block: notarize a packaged macOS release artifact")]
[group('release-engineering')]
[macos]
release-notarize-macos archive profile=notary-profile:
    test "{{codesign-identity}}" != "-" || (echo "Notarization requires THANE_CODESIGN_IDENTITY to name a Developer ID Application certificate" && exit 1)
    test -n "{{installer-identity}}" && test "{{installer-identity}}" != "-" || (echo "Notarization of macOS installer packages requires THANE_INSTALLER_IDENTITY to name a Developer ID Installer certificate" && exit 1)
    test -n "{{profile}}" || (echo "Set THANE_NOTARY_PROFILE or pass a notary profile name" && exit 1)
    xcrun notarytool submit "{{archive}}" --keychain-profile "{{profile}}" --wait

[doc("Building block: package a macOS binary as a signed flat installer product archive")]
[group('release-engineering')]
[macos]
release-package-macos-pkg version target_arch binary output_dir=release-dir installer_identity=installer-identity:
    scripts/package-macos-pkg.sh "{{version}}" "{{target_arch}}" "{{binary}}" "{{output_dir}}" "{{installer_identity}}"

[doc("Building block: staple and validate a notarized macOS installer package")]
[group('release-engineering')]
[macos]
release-staple-macos archive:
    xcrun stapler staple "{{archive}}"
    xcrun stapler validate "{{archive}}"

[doc("Building block: build one release artifact for a target")]
[group('release-engineering')]
release-build-archive version target_os=host_os target_arch=host_arch cc="":
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

    if [ "$target_os" = "darwin" ]; then
        test "{{host_os}}" = "darwin" || { echo "release-build-archive for darwin targets requires a macOS host"; exit 1; }
        just release-sign-macos "$binary"
        if [ -n "${THANE_NOTARY_PROFILE:-}" ] && { [ -z "{{installer-identity}}" ] || [ "{{installer-identity}}" = "-" ]; }; then
            echo "Set THANE_INSTALLER_IDENTITY to a Developer ID Installer certificate before notarizing macOS release packages." >&2
            exit 1
        fi
        archive="$(just release-package-macos-pkg "v${version}" "$target_arch" "$binary" | tail -n 1)"
        test -n "$archive" || { echo "release-package-macos-pkg did not report an artifact path" >&2; exit 1; }
        if [ -n "${THANE_NOTARY_PROFILE:-}" ]; then
            just release-notarize-macos "$archive"
            just release-staple-macos "$archive"
        fi
    else
        archive="$(scripts/package-release.sh "$version" "$target_os" "$target_arch" "$binary" "{{release-dir}}")"
    fi

    printf '%s\n' "$archive"

[doc("Building block: write checksums for prepared release artifacts")]
[group('release-engineering')]
release-write-checksums version:
    #!/usr/bin/env bash
    set -euo pipefail
    version="{{version}}"
    version="${version#v}"
    output="{{release-dir}}/thane_${version}_checksums.txt"

    cd "{{release-dir}}"
    shopt -s nullglob
    archives=("./thane_${version}_"*.tar.gz "./thane_${version}_"*.pkg)
    if [ "${#archives[@]}" -eq 0 ]; then
        echo "No release artifacts found for version ${version} in {{release-dir}}" >&2
        exit 1
    fi
    if [ "${#archives[@]}" -ne 4 ]; then
        echo "Expected 4 release artifacts for version ${version}, found ${#archives[@]} in {{release-dir}}" >&2
        printf '  %s\n' "${archives[@]}" >&2
        exit 1
    fi

    sorted_archives="$(printf '%s\n' "${archives[@]#./}" | LC_ALL=C sort)"
    rm -f "$(basename "$output")"

    if command -v sha256sum >/dev/null 2>&1; then
        printf '%s\n' "$sorted_archives" | xargs sha256sum > "$(basename "$output")"
    else
        printf '%s\n' "$sorted_archives" | xargs shasum -a 256 > "$(basename "$output")"
    fi

    printf '%s\n' "$output"

[doc("Building block: build one local snapshot archive")]
[group('release-engineering')]
release-build-snapshot version target_os=host_os target_arch=host_arch cc="":
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -n "{{cc}}" ]; then
        just release-build-archive "{{version}}" "{{target_os}}" "{{target_arch}}" "{{cc}}"
    else
        just release-build-archive "{{version}}" "{{target_os}}" "{{target_arch}}"
    fi
    just release-write-checksums "{{version}}"

[doc("Building block: build one Linux archive via Docker Buildx")]
[group('release-engineering')]
release-build-linux-archive version target_arch:
    #!/usr/bin/env bash
    set -euo pipefail
    version="{{version}}"
    target_arch="{{target_arch}}"

    version="${version#v}"
    THANE_VERSION="v${version}" just build-linux-docker "$target_arch"

    archive="$(scripts/package-release.sh "$version" linux "$target_arch" "dist/thane-linux-${target_arch}" "{{release-dir}}")"
    printf '%s\n' "$archive"

[doc("Building block: validate GitHub auth and prepared assets")]
[group('release-engineering')]
release-github-check version:
    #!/usr/bin/env bash
    set -euo pipefail
    version="{{version}}"
    version="${version#v}"
    metadata_path="{{release-dir}}/.thane_${version}_prepared.env"
    checksum_path="{{release-dir}}/thane_${version}_checksums.txt"
    assets=(
        "{{release-dir}}/thane_${version}_darwin_amd64.pkg"
        "{{release-dir}}/thane_${version}_darwin_arm64.pkg"
        "{{release-dir}}/thane_${version}_linux_amd64.tar.gz"
        "{{release-dir}}/thane_${version}_linux_arm64.tar.gz"
        "{{release-dir}}/thane_${version}_checksums.txt"
    )

    if [ -z "${THANE_GH_TOKEN:-}" ]; then
        echo "Set THANE_GH_TOKEN to a GitHub token for the intended release identity before uploading release assets." >&2
        exit 1
    fi

    if ! command -v gh >/dev/null 2>&1; then
        echo "GitHub CLI (gh) is required for release uploads" >&2
        exit 1
    fi

    GH_TOKEN="${THANE_GH_TOKEN}" gh api user --jq '.login' >/dev/null

    missing=0
    for asset in "${assets[@]}"; do
        if [ ! -f "$asset" ]; then
            echo "Missing release asset: $asset" >&2
            missing=1
        fi
    done

    if [ "$missing" -ne 0 ]; then
        echo "Run 'just prepare-release ${version}' on the macOS release workstation before uploading assets." >&2
        exit 1
    fi

    if [ ! -f "$metadata_path" ]; then
        echo "Missing release metadata: $metadata_path" >&2
        echo "Run 'just prepare-release ${version}' on the macOS release workstation before publishing." >&2
        exit 1
    fi

    checksum_assets="$(awk '{print $NF}' "$checksum_path" | LC_ALL=C sort)"
    expected_checksum_assets=(
        "thane_${version}_darwin_amd64.pkg"
        "thane_${version}_darwin_arm64.pkg"
        "thane_${version}_linux_amd64.tar.gz"
        "thane_${version}_linux_arm64.tar.gz"
    )
    checksum_asset_count="$(printf '%s\n' "$checksum_assets" | sed '/^$/d' | wc -l | tr -d ' ')"

    if [ "$checksum_asset_count" -ne "${#expected_checksum_assets[@]}" ]; then
        echo "Checksum file $checksum_path should describe ${#expected_checksum_assets[@]} release artifacts, found ${checksum_asset_count} entries." >&2
        printf '  %s\n' "$checksum_assets" >&2
        exit 1
    fi

    for expected_asset in "${expected_checksum_assets[@]}"; do
        if ! printf '%s\n' "$checksum_assets" | grep -Fxq "$expected_asset"; then
            echo "Checksum file $checksum_path is missing expected asset: $expected_asset" >&2
            exit 1
        fi
    done

[doc("Building block: create or update the GitHub release from prepared assets")]
[group('release-engineering')]
release-github-upload version target_commit="" release_kind="auto":
    #!/usr/bin/env bash
    set -euo pipefail
    version="{{version}}"
    version="${version#v}"
    tag="v${version}"
    target_commit="{{target_commit}}"
    release_kind="{{release_kind}}"
    release_exists=0
    prerelease=0
    release_id=""
    current_prerelease=""
    current_draft=""
    is_immutable=""
    assets=(
        "{{release-dir}}/thane_${version}_darwin_amd64.pkg"
        "{{release-dir}}/thane_${version}_darwin_arm64.pkg"
        "{{release-dir}}/thane_${version}_linux_amd64.tar.gz"
        "{{release-dir}}/thane_${version}_linux_arm64.tar.gz"
        "{{release-dir}}/thane_${version}_checksums.txt"
    )

    just --quiet release-github-check "$version"
    export GH_TOKEN="${THANE_GH_TOKEN}"

    case "$release_kind" in
        auto)
            if printf '%s' "$version" | grep -q -- '-'; then
                prerelease=1
            fi
            ;;
        prerelease)
            prerelease=1
            ;;
        release)
            prerelease=0
            ;;
        *)
            echo "release_kind must be one of: auto, prerelease, release" >&2
            exit 1
            ;;
    esac

    create_args=("${tag}" --title "${tag}" --generate-notes)
    if [ "$prerelease" -eq 1 ]; then
        create_args+=(--prerelease)
        create_args+=(--latest=false)
    fi
    if [ -n "$target_commit" ]; then
        create_args+=(--target "$target_commit")
    else
        create_args+=(--verify-tag)
    fi

    if gh release view "$tag" --json id,isImmutable,isDraft,isPrerelease >/dev/null 2>&1; then
        release_exists=1
        release_id="$(gh release view "$tag" --json id --jq '.id')"
        current_prerelease="$(gh release view "$tag" --json isPrerelease --jq '.isPrerelease')"
        current_draft="$(gh release view "$tag" --json isDraft --jq '.isDraft')"
        is_immutable="$(gh release view "$tag" --json isImmutable --jq '.isImmutable')"
    fi

    if [ "$release_exists" -eq 0 ]; then
        gh release create "${create_args[@]}" "${assets[@]}"
        exit 0
    fi

    if [ "$is_immutable" = "true" ]; then
        remote_assets="$(gh release view "$tag" --json assets --jq '.assets[].name' 2>/dev/null || true)"
        missing_remote=0
        for asset in "${assets[@]}"; do
            asset_name="$(basename "$asset")"
            if ! printf '%s\n' "$remote_assets" | grep -Fxq "$asset_name"; then
                echo "Immutable release $tag is missing asset: $asset_name" >&2
                missing_remote=1
            fi
        done

        if [ "$missing_remote" -ne 0 ]; then
            echo "Release $tag is already published and immutable, so the missing assets cannot be repaired in place." >&2
            echo "Cut a new version instead." >&2
            exit 1
        fi

        desired_prerelease="false"
        if [ "$prerelease" -eq 1 ]; then
            desired_prerelease="true"
        fi
        if [ "$current_prerelease" != "$desired_prerelease" ]; then
            echo "Immutable release $tag already exists with prerelease=$current_prerelease; cannot change it to $desired_prerelease." >&2
            exit 1
        fi

        echo "Release $tag is already published and immutable with the expected assets. Treating upload as idempotent."
        exit 0
    fi

    gh release upload "$tag" "${assets[@]}" --clobber

    if [ "$current_draft" = "true" ]; then
        gh release edit "$tag" --draft=false
    fi

    desired_prerelease=false
    if [ "$prerelease" -eq 1 ]; then
        desired_prerelease=true
    fi
    if [ "$current_prerelease" != "$desired_prerelease" ]; then
        repo="$(gh repo view --json nameWithOwner --jq '.nameWithOwner')"
        gh api \
            --method PATCH \
            "repos/${repo}/releases/${release_id}" \
            -F prerelease="${desired_prerelease}" \
            >/dev/null
    fi

[doc("Operator path: build, sign/notarize, package, checksum, and smoke-test the release locally")]
[group('release-engineering')]
prepare-release version container_tag="thane:prepare-release":
    #!/usr/bin/env bash
    set -euo pipefail
    version="{{version}}"
    container_tag="{{container_tag}}"
    metadata_path=""

    version="${version#v}"

    if ! printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'; then
        echo "Version must look like 0.9.0 or 0.9.0-rc.1" >&2
        exit 1
    fi

    test "{{host_os}}" = "darwin" || { echo "prepare-release must run on a macOS release workstation"; exit 1; }
    test -z "$(git status --short)" || { echo "Worktree must be clean before a prepare-release run"; exit 1; }

    mkdir -p "{{release-dir}}"
    rm -f \
        "{{release-dir}}/thane_${version}_"*.pkg \
        "{{release-dir}}/thane_${version}_"*.tar.gz \
        "{{release-dir}}/thane_${version}_"*.zip \
        "{{release-dir}}/thane_${version}_checksums.txt" \
        "{{release-dir}}/.thane_${version}_prepared.env"

    just ci

    just release-build-archive "v${version}" darwin amd64
    just release-build-archive "v${version}" darwin arm64
    just release-build-linux-archive "v${version}" amd64
    just release-build-linux-archive "v${version}" arm64
    just release-write-checksums "v${version}"

    just container "$container_tag"
    docker run --rm "$container_tag" version

    metadata_path="{{release-dir}}/.thane_${version}_prepared.env"
    printf 'THANE_RELEASE_PREPARED_VERSION=%s\n' "$version" > "$metadata_path"
    printf 'THANE_RELEASE_PREPARED_COMMIT=%s\n' "$(git rev-parse HEAD)" >> "$metadata_path"
    printf 'THANE_RELEASE_PREPARED_BRANCH=%s\n' "$(git rev-parse --abbrev-ref HEAD)" >> "$metadata_path"
    printf 'THANE_RELEASE_PREPARED_AT=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" >> "$metadata_path"

    echo ""
    echo "Local release preparation complete."
    echo "  Release artifacts/checksums: {{release-dir}}/"
    echo "  Release metadata: $metadata_path"
    echo "  Included release artifacts: darwin/amd64 pkg, darwin/arm64 pkg, linux/amd64 tar.gz, linux/arm64 tar.gz"
    echo "  Container smoke tag: $container_tag"
    echo ""
    echo "Nothing was tagged, pushed, or uploaded to GitHub."
    echo "If THANE_NOTARY_PROFILE was set, Apple notarization and stapling were completed during this run."
    echo "Next off-machine step when ready:"
    echo "  just publish-release v${version}"

[doc("Operator path: create the release tag and publish prepared assets to GitHub")]
[group('release-engineering')]
publish-release version release_kind="auto":
    #!/usr/bin/env bash
    set -euo pipefail
    version="{{version}}"
    version="${version#v}"
    tag="v${version}"
    release_kind="{{release_kind}}"
    metadata_path="{{release-dir}}/.thane_${version}_prepared.env"
    repo=""
    head_commit="$(git rev-parse HEAD)"
    force_release="${THANE_RELEASE_FORCE:-false}"
    remote_tag_commit=""

    if ! printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'; then
        echo "Version must look like 0.9.0 or 0.9.0-rc.1" >&2
        exit 1
    fi

    test -z "$(git status --short)" || { echo "Worktree must be clean before cutting a release"; exit 1; }

    just --quiet release-github-check "$tag"
    export GH_TOKEN="${THANE_GH_TOKEN}"
    . "$metadata_path"

    test "${THANE_RELEASE_PREPARED_VERSION:-}" = "$version" || {
        echo "Prepared release metadata version is '${THANE_RELEASE_PREPARED_VERSION:-}', expected '${version}'." >&2
        echo "Run 'just prepare-release ${version}' before publishing." >&2
        exit 1
    }
    test -n "${THANE_RELEASE_PREPARED_COMMIT:-}" || {
        echo "Prepared release metadata is missing THANE_RELEASE_PREPARED_COMMIT." >&2
        echo "Run 'just prepare-release ${version}' before publishing." >&2
        exit 1
    }
    test "$THANE_RELEASE_PREPARED_COMMIT" = "$head_commit" || {
        echo "Prepared release assets were built from ${THANE_RELEASE_PREPARED_COMMIT}, but current HEAD is ${head_commit}." >&2
        echo "Run 'just prepare-release ${version}' from this commit before publishing." >&2
        exit 1
    }

    git fetch origin main
    if [ "$force_release" = "true" ]; then
        echo "THANE_RELEASE_FORCE=true: bypassing main-branch and origin/main release guards for release-engineering testing."
    else
        test "$(git rev-parse --abbrev-ref HEAD)" = "main" || { echo "Release tags must be cut from main (or set THANE_RELEASE_FORCE=true for releng testing)"; exit 1; }
        test "$head_commit" = "$(git rev-parse origin/main)" || { echo "Local main must match origin/main before release (or set THANE_RELEASE_FORCE=true for releng testing)"; exit 1; }
    fi

    repo="$(gh repo view --json nameWithOwner --jq '.nameWithOwner')"
    if remote_tag_type="$(gh api "repos/${repo}/git/ref/tags/${tag}" --jq '.object.type' 2>/dev/null)"; then
        remote_tag_sha="$(gh api "repos/${repo}/git/ref/tags/${tag}" --jq '.object.sha')"
        if [ "$remote_tag_type" = "tag" ]; then
            remote_tag_commit="$(gh api "repos/${repo}/git/tags/${remote_tag_sha}" --jq '.object.sha')"
        elif [ "$remote_tag_type" = "commit" ]; then
            remote_tag_commit="$remote_tag_sha"
        else
            echo "Unsupported remote tag object type for $tag: $remote_tag_type" >&2
            exit 1
        fi
    fi

    if [ -n "$remote_tag_commit" ]; then
        if [ "$remote_tag_commit" != "$head_commit" ]; then
            echo "Remote tag $tag already exists but points to $remote_tag_commit; expected $head_commit" >&2
            exit 1
        fi

        echo "Remote tag $tag already exists at the current commit. Treating publish as idempotent."
        just release-github-upload "$tag" "" "$release_kind"
        echo "Uploaded local release artifacts/checksums. GitHub Actions can publish or republish the container image separately."
        exit 0
    fi

    just release-github-upload "$tag" "$head_commit" "$release_kind"
    echo "Created $tag via the GitHub release API, uploaded local release artifacts/checksums, and triggered GitHub Actions to publish the container image."

[doc("Operator path: build, notarize, and publish a GitHub release from a clean main checkout (release_kind: auto|prerelease|release)")]
[group('release-engineering')]
[macos]
release-github version release_kind="auto" container_tag="thane:prepare-release":
    scripts/releng/release-github.sh "{{version}}" "{{release_kind}}" "{{container_tag}}"

[private]
macos-sign binary identity=codesign-identity options=codesign-options timestamp=codesign-timestamp:
    just release-sign-macos "{{binary}}" "{{identity}}" "{{options}}" "{{timestamp}}"

[private]
[macos]
macos-notarize archive profile=notary-profile:
    just release-notarize-macos "{{archive}}" "{{profile}}"

[private]
[macos]
macos-staple archive:
    just release-staple-macos "{{archive}}"

[private]
release-archive version target_os=host_os target_arch=host_arch cc="":
    just release-build-archive "{{version}}" "{{target_os}}" "{{target_arch}}" "{{cc}}"

[private]
release-checksums version:
    just release-write-checksums "{{version}}"

[private]
release-snapshot version target_os=host_os target_arch=host_arch cc="":
    just release-build-snapshot "{{version}}" "{{target_os}}" "{{target_arch}}" "{{cc}}"

[private]
release-archive-linux-docker version target_arch:
    just release-build-linux-archive "{{version}}" "{{target_arch}}"

[private]
release-upload-validate version:
    just release-github-check "{{version}}"

[private]
release-upload version release_kind="auto":
    just release-github-upload "{{version}}" "" "{{release_kind}}"

[private]
release-breakpoint version container_tag="thane:release-breakpoint":
    just prepare-release "{{version}}" "{{container_tag}}"

[private]
release version release_kind="auto":
    just publish-release "{{version}}" "{{release_kind}}"
