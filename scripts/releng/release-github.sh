#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
# shellcheck source=./common.sh
source "$script_dir/common.sh"

if [[ $# -lt 1 || $# -gt 3 ]]; then
    die "usage: $0 <version> [release-kind] [container-tag]"
fi

version="$(normalize_version "$1")"
release_kind="${2:-auto}"
container_tag="${3:-thane:prepare-release}"

cd "$repo_root"
require_macos_host
require_commands just docker gh codesign pkgbuild productbuild pkgutil lsbom mkbom cpio gzip xcrun

validate_release_version "$version"
validate_release_kind "$release_kind"
require_clean_worktree "cutting a GitHub release"
require_main_branch
require_origin_main_match
require_real_codesign_identity
require_real_installer_identity
require_notary_profile
require_github_token

prerelease="$(resolve_prerelease_bool "$version" "$release_kind")"

section "Cut GitHub release"
step "Version: v$version"
step "Release kind: $release_kind (prerelease=${prerelease})"
step "Container smoke tag: $container_tag"

section "Prepare release artifacts"
run just prepare-release "v$version" "$container_tag"

section "Publish GitHub release"
run just publish-release "v$version" "$release_kind"

section "Release complete"
step "GitHub release v$version is published"
