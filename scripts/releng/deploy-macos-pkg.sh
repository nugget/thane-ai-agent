#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
# shellcheck source=./common.sh
source "$script_dir/common.sh"

if [[ $# -lt 1 || $# -gt 5 ]]; then
    die "usage: $0 <user@host> [target-arch] [version] [remote-pkg-dir] [restart-cmd]"
fi

host="$1"
target_arch="${2:-arm64}"
version_input="${3:-}"
remote_pkg_dir="${4:-/tmp/thane-releng}"
restart_cmd="${5:-}"

if [[ -z "$restart_cmd" ]]; then
    restart_cmd='launchctl kickstart -k gui/$(id -u)/info.nugget.thane'
fi

cd "$repo_root"
require_macos_host
require_commands just ssh scp
require_clean_worktree "building and deploying a macOS pkg"
require_real_codesign_identity
require_real_installer_identity

section "Build macOS installer package"
pkg_path="$("$script_dir/build-macos-pkg.sh" "$version_input" "$target_arch" "dist/pkg" true | tail -n 1)"
pkg_name="$(basename "$pkg_path")"
remote_pkg_path="${remote_pkg_dir}/${pkg_name}"

section "Deploy package to remote host"
step "Remote host: $host"
step "Remote staging path: $remote_pkg_path"
step "Remote install domain: CurrentUserHomeDirectory"

run ssh "$host" "mkdir -p '$remote_pkg_dir'"
run scp -p "$pkg_path" "${host}:${remote_pkg_path}"
run ssh "$host" "pkgutil --check-signature '$remote_pkg_path'"

run ssh "$host" "installer -pkg '$remote_pkg_path' -target CurrentUserHomeDirectory"

if [[ -n "$restart_cmd" ]]; then
    run ssh "$host" "$restart_cmd"
fi

run ssh "$host" "rm -f '$remote_pkg_path'"
run ssh "$host" "~/Thane/bin/thane version"

section "Deployment complete"
step "Installed $(basename "$pkg_path") on $host and refreshed the running service"
