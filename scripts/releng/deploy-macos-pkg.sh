#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
# shellcheck source=./common.sh
source "$script_dir/common.sh"

if [[ $# -lt 1 || $# -gt 7 ]]; then
    die "usage: $0 <user@host> [target-arch] [version] [remote-pkg-dir] [restart-cmd] [verify-url] [verify-timeout-seconds]"
fi

host="$1"
target_arch="${2:-arm64}"
version_input="${3:-}"
remote_pkg_dir="${4:-/tmp/thane-releng}"
restart_cmd="${5:-}"
verify_url="${6:-http://127.0.0.1:8080/v1/version}"
verify_timeout_seconds="${7:-60}"

if [[ -z "$restart_cmd" ]]; then
    restart_cmd='launchctl kickstart -k gui/$(id -u)/info.nugget.thane'
fi

cd "$repo_root"
require_macos_host
require_commands just ssh scp
require_clean_worktree "building and deploying a macOS pkg"
require_real_codesign_identity
require_real_installer_identity

version="${version_input:-$(git_describe_version)}"
expected_version="v${version#v}"

section "Build macOS installer package"
pkg_path="$("$script_dir/build-macos-pkg.sh" "$version" "$target_arch" "dist/pkg" true | tail -n 1)"
pkg_name="$(basename "$pkg_path")"
remote_pkg_path="${remote_pkg_dir}/${pkg_name}"

section "Deploy package to remote host"
step "Remote host: $host"
step "Remote staging path: $remote_pkg_path"
step "Remote install domain: CurrentUserHomeDirectory"
step "Expected live version: $expected_version"
step "Verify URL: $verify_url"

run ssh "$host" "mkdir -p '$remote_pkg_dir'"
run scp -p "$pkg_path" "${host}:${remote_pkg_path}"
run ssh "$host" "pkgutil --check-signature '$remote_pkg_path'"

run ssh "$host" "installer -pkg '$remote_pkg_path' -target CurrentUserHomeDirectory"

if [[ -n "$restart_cmd" ]]; then
    run ssh "$host" "$restart_cmd"
fi

run ssh "$host" "rm -f '$remote_pkg_path'"

section "Verify remote live version"
step "Waiting up to ${verify_timeout_seconds}s for $verify_url to report $expected_version"

deadline=$((SECONDS + verify_timeout_seconds))
last_status="remote API not ready yet"
while (( SECONDS < deadline )); do
    if remote_version="$(
        ssh "$host" /usr/bin/python3 - "$verify_url" <<'PY'
import json
import sys
import urllib.request

url = sys.argv[1]
with urllib.request.urlopen(url, timeout=5) as resp:
    data = json.load(resp)
print(data.get("version", ""))
PY
    )"; then
        remote_version="${remote_version//$'\r'/}"
        remote_version="${remote_version//$'\n'/}"
        if [[ "$remote_version" == "$expected_version" ]]; then
            step "Remote API reports version: $remote_version"
            section "Deployment complete"
            step "Installed $(basename "$pkg_path") on $host and verified the running Thane API"
            exit 0
        fi
        last_status="remote API reported version '$remote_version'"
    else
        last_status="remote API not ready yet"
    fi
    sleep 2
done

die "timed out after ${verify_timeout_seconds}s waiting for $verify_url on $host to report version $expected_version (${last_status})"
