#!/usr/bin/env bash
set -euo pipefail

IFS=$'\n\t'

if [ "$#" -ne 5 ]; then
    echo "usage: $0 <version> <arch> <binary-path> <output-dir> <installer-identity>" >&2
    exit 1
fi

version="${1#v}"
target_arch="$2"
binary_path="$3"
output_dir="$4"
installer_identity="$5"

if [ ! -f "$binary_path" ]; then
    echo "binary not found: $binary_path" >&2
    exit 1
fi

mkdir -p "$output_dir"

output_dir="$(cd "$output_dir" && pwd)"
package_name="thane_${version}_darwin_${target_arch}.pkg"
stage_dir="$(mktemp -d "${TMPDIR:-/tmp}/thane-pkg.XXXXXX")"
payload_root="$stage_dir/root"

cleanup() {
    rm -rf "$stage_dir"
}
trap cleanup EXIT

mkdir -p "$payload_root/usr/local/bin"
install -m 755 "$binary_path" "$payload_root/usr/local/bin/thane"
if command -v xattr >/dev/null 2>&1; then
    xattr -cr "$payload_root"
fi

args=(
    pkgbuild
    --root "$payload_root"
    --identifier "info.nugget.thane"
    --version "$version"
    --install-location "/"
)
if [ -n "$installer_identity" ] && [ "$installer_identity" != "-" ]; then
    args+=(--sign "$installer_identity")
fi
artifact_path="$output_dir/$package_name"
args+=("$artifact_path")

# Keep stdout reserved for the final artifact path so release recipes can
# safely capture this script's result with command substitution.
COPYFILE_DISABLE=1 "${args[@]}" >&2

if [ -n "$installer_identity" ] && [ "$installer_identity" != "-" ]; then
    pkgutil --check-signature "$artifact_path" >&2
fi

printf '%s\n' "$artifact_path"
