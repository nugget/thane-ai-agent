#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
# shellcheck source=./common.sh
source "$script_dir/common.sh"

if [[ $# -gt 4 ]]; then
    die "usage: $0 [version] [target-arch] [output-dir] [require-signed]"
fi

version_input="${1:-}"
target_arch="${2:-arm64}"
output_dir="${3:-dist/pkg}"
require_signed="${4:-true}"

cd "$repo_root"
require_macos_host
require_commands just codesign pkgbuild productbuild pkgutil

case "$target_arch" in
    amd64|arm64) ;;
    *) die "unsupported macOS target architecture: $target_arch" ;;
esac

version="${version_input:-$(git_describe_version)}"
build_version="v${version#v}"

case "$version" in
    dev) warn "using fallback version label 'dev' because git describe did not find a tag" ;;
esac

require_clean_worktree "building a macOS pkg from the current checkout"

case "$require_signed" in
    true)
        require_real_codesign_identity
        require_real_installer_identity
        ;;
    false) ;;
    *) die "require-signed must be true or false" ;;
esac

section "Build macOS installer package"
step "Branch: $(git rev-parse --abbrev-ref HEAD)"
step "Version: $version"
step "Embedded build version: $build_version"
step "Architecture: $target_arch"
step "Output directory: $output_dir"
step "Signing required: $require_signed"

binary="dist/thane-darwin-${target_arch}"

run env THANE_VERSION="$build_version" just build darwin "$target_arch"
run just release-sign-macos "$binary"

mkdir -p "$output_dir"
step "Packaging signed installer product"
pkg_path="$(just release-package-macos-pkg "$version" "$target_arch" "$binary" "$output_dir" | tail -n 1)"
[[ -n "$pkg_path" ]] || die "release-package-macos-pkg did not report an artifact path"

step "Package ready: $pkg_path"
printf '%s\n' "$pkg_path"
