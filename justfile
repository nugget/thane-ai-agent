pkg := "github.com/nugget/thane-ai-agent/internal/buildinfo"
version := `git describe --tags --always --dirty 2>/dev/null || echo "dev"`
git_commit := `git rev-parse --short HEAD 2>/dev/null || echo "unknown"`
git_branch := `git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown"`
build_time := `date -u '+%Y-%m-%dT%H:%M:%SZ'`

ldflags := "-X " + pkg + ".Version=" + version + " -X " + pkg + ".GitCommit=" + git_commit + " -X " + pkg + ".GitBranch=" + git_branch + " -X " + pkg + ".BuildTime=" + build_time

host_os := if os() == "macos" { "darwin" } else { os() }
host_arch := if arch() == "aarch64" { "arm64" } else if arch() == "x86_64" { "amd64" } else { arch() }

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

# Clean build artifacts
clean:
    rm -rf dist

# Tag and publish a GitHub release (usage: just release 0.2.0)
release tag:
    git tag -a "v{{tag}}" -m "Release v{{tag}}"
    git push origin "v{{tag}}"
    gh release create "v{{tag}}" --generate-notes --title "v{{tag}}"
