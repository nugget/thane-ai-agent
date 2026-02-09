# Thane Development Justfile
# Run `just` or `just help` to see available recipes

set dotenv-load := false

# Default: show help
default:
    @just --list

# ─────────────────────────────────────────────────────────────────────────────
# Building
# ─────────────────────────────────────────────────────────────────────────────

# Build thane binary
build:
    go build -o thane ./cmd/thane

# Build with version info embedded
build-release version="dev":
    go build -ldflags "-X main.Version={{version}}" -o thane ./cmd/thane

# Build for specific platform (e.g., just cross linux amd64)
cross os arch:
    GOOS={{os}} GOARCH={{arch}} go build -o thane-{{os}}-{{arch}} ./cmd/thane

# Build multi-arch binaries
build-all:
    just cross linux amd64
    just cross linux arm64
    just cross darwin amd64
    just cross darwin arm64

# ─────────────────────────────────────────────────────────────────────────────
# Testing & Quality
# ─────────────────────────────────────────────────────────────────────────────

# Run all tests
test:
    go test ./...

# Run tests with verbose output
test-v:
    go test -v ./...

# Run tests with race detector
test-race:
    go test -race ./...

# Run tests for specific package (e.g., just test-pkg ./internal/anticipation/...)
test-pkg pkg:
    go test -v {{pkg}}

# Format code
fmt:
    gofmt -w .

# Check formatting without modifying
fmt-check:
    @echo "Checking gofmt..."
    @test -z "$(gofmt -l .)" || (echo "Files need formatting:"; gofmt -l .; exit 1)

# Run linter (installs golangci-lint if needed)
lint:
    #!/usr/bin/env bash
    set -euo pipefail
    export PATH="$PATH:$(go env GOPATH)/bin"
    if ! command -v golangci-lint &> /dev/null; then
        echo "Installing golangci-lint..."
        go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
    fi
    golangci-lint run ./...

# Run all checks (fmt + lint + test)
check: fmt-check lint test
    @echo "✅ All checks passed"

# ─────────────────────────────────────────────────────────────────────────────
# Git Workflow
# ─────────────────────────────────────────────────────────────────────────────

# Pre-push checks: format, lint, test, then push
push branch="": check
    #!/usr/bin/env bash
    set -euo pipefail
    BRANCH="${1:-$(git branch --show-current)}"
    echo "Pushing $BRANCH..."
    git push origin "$BRANCH"

# Format, commit with message, and push
ship message: fmt
    git add -A
    git commit -m "{{message}}"
    just push

# Show git status and recent commits  
status:
    @git status --short
    @echo ""
    @git log --oneline -5

# ─────────────────────────────────────────────────────────────────────────────
# Running
# ─────────────────────────────────────────────────────────────────────────────

# Run thane server (builds first)
run: build
    ./thane serve

# Run with specific config
run-config config: build
    ./thane -config {{config}} serve

# Run chat interface
chat: build
    ./thane chat

# ─────────────────────────────────────────────────────────────────────────────
# Dependencies & Maintenance  
# ─────────────────────────────────────────────────────────────────────────────

# Tidy go modules
tidy:
    go mod tidy

# Update dependencies
update:
    go get -u ./...
    go mod tidy

# Clean build artifacts
clean:
    rm -f thane thane-*-*
    go clean

# Show module dependency graph
deps:
    go mod graph | head -50

# ─────────────────────────────────────────────────────────────────────────────
# Docker
# ─────────────────────────────────────────────────────────────────────────────

# Build Docker image
docker-build tag="thane:latest":
    docker build -t {{tag}} .

# Build multi-arch Docker image
docker-buildx tag="thane:latest":
    docker buildx build --platform linux/amd64,linux/arm64 -t {{tag}} .

# ─────────────────────────────────────────────────────────────────────────────
# Development Helpers
# ─────────────────────────────────────────────────────────────────────────────

# Generate test coverage report
coverage:
    go test -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out -o coverage.html
    @echo "Coverage report: coverage.html"

# Watch for changes and run tests (requires entr)
watch:
    find . -name '*.go' | entr -c go test ./...

# Show lines of code
loc:
    @find . -name '*.go' -not -path './vendor/*' | xargs wc -l | tail -1

# Quick sanity check for CI parity
ci: fmt-check lint test-race
    @echo "✅ CI checks passed"
