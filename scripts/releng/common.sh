#!/usr/bin/env bash
set -euo pipefail

# Shared helpers for human-facing release-engineering scripts. Keep the
# progress output legible and fail early on missing prerequisites so the
# operator does not have to reconstruct state from half-completed shell output.

if [[ -t 1 ]]; then
    _RELENG_BOLD=$'\033[1m'
    _RELENG_BLUE=$'\033[34m'
    _RELENG_CYAN=$'\033[36m'
    _RELENG_YELLOW=$'\033[33m'
    _RELENG_RED=$'\033[31m'
    _RELENG_RESET=$'\033[0m'
else
    _RELENG_BOLD=""
    _RELENG_BLUE=""
    _RELENG_CYAN=""
    _RELENG_YELLOW=""
    _RELENG_RED=""
    _RELENG_RESET=""
fi

section() {
    printf '\n%s%s==> %s%s\n' "$_RELENG_BOLD" "$_RELENG_BLUE" "$*" "$_RELENG_RESET"
}

step() {
    printf '%s%s -> %s%s\n' "$_RELENG_BOLD" "$_RELENG_CYAN" "$*" "$_RELENG_RESET"
}

warn() {
    printf '%s%s !! %s%s\n' "$_RELENG_BOLD" "$_RELENG_YELLOW" "$*" "$_RELENG_RESET" >&2
}

die() {
    printf '%s%s !! %s%s\n' "$_RELENG_BOLD" "$_RELENG_RED" "$*" "$_RELENG_RESET" >&2
    exit 1
}

run() {
    step "$*"
    "$@"
}

require_command() {
    local cmd="$1"
    command -v "$cmd" >/dev/null 2>&1 || die "required command not found: $cmd"
}

require_commands() {
    local cmd
    for cmd in "$@"; do
        require_command "$cmd"
    done
}

require_macos_host() {
    [[ "$(uname -s)" == "Darwin" ]] || die "this workflow must run on a macOS host"
}

normalize_version() {
    local version="$1"
    printf '%s\n' "${version#v}"
}

validate_release_version() {
    local version="$1"
    [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]] || \
        die "version must look like 0.9.0 or 0.9.0-rc.1"
}

git_describe_version() {
    git describe --tags --always --dirty 2>/dev/null || echo dev
}

worktree_dirty() {
    [[ -n "$(git status --short)" ]]
}

require_clean_worktree() {
    local context="${1:-operation}"
    if worktree_dirty; then
        die "worktree must be clean before ${context}"
    fi
}

warn_if_dirty_worktree() {
    local context="${1:-operation}"
    if worktree_dirty; then
        warn "worktree is dirty; continuing with ${context}"
        git status --short
    fi
}

require_main_branch() {
    local branch
    branch="$(git rev-parse --abbrev-ref HEAD)"
    [[ "$branch" == "main" ]] || die "this workflow must run from main (current branch: $branch)"
}

require_origin_main_match() {
    run git fetch origin main --tags
    local head_commit origin_main
    head_commit="$(git rev-parse HEAD)"
    origin_main="$(git rev-parse origin/main)"
    [[ "$head_commit" == "$origin_main" ]] || die "local main must match origin/main before cutting a release"
}

require_real_codesign_identity() {
    local identity="${THANE_CODESIGN_IDENTITY:-}"
    [[ -n "$identity" && "$identity" != "-" ]] || \
        die "set THANE_CODESIGN_IDENTITY to a real Developer ID Application certificate"
}

require_real_installer_identity() {
    local identity="${THANE_INSTALLER_IDENTITY:-}"
    [[ -n "$identity" && "$identity" != "-" ]] || \
        die "set THANE_INSTALLER_IDENTITY to a real Developer ID Installer certificate"
}

require_notary_profile() {
    [[ -n "${THANE_NOTARY_PROFILE:-}" ]] || \
        die "set THANE_NOTARY_PROFILE to a notarytool keychain profile"
}

require_github_token() {
    [[ -n "${THANE_GH_TOKEN:-}" ]] || die "set THANE_GH_TOKEN before publishing GitHub releases"
}

validate_release_kind() {
    local kind="$1"
    case "$kind" in
        auto|prerelease|release) ;;
        *) die "release_kind must be one of: auto, prerelease, release" ;;
    esac
}

resolve_prerelease_bool() {
    local version="$1"
    local kind="$2"
    case "$kind" in
        prerelease) printf 'true\n' ;;
        release) printf 'false\n' ;;
        auto)
            if [[ "$version" == *-* ]]; then
                printf 'true\n'
            else
                printf 'false\n'
            fi
            ;;
    esac
}
