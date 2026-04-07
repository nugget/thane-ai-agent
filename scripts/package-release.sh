#!/usr/bin/env bash
set -euo pipefail

IFS=$'\n\t'

if [ "$#" -ne 5 ]; then
    echo "usage: $0 <version> <os> <arch> <binary-path> <output-dir>" >&2
    exit 1
fi

version="${1#v}"
target_os="$2"
target_arch="$3"
binary_path="$4"
output_dir="$5"

if [ ! -f "$binary_path" ]; then
    echo "binary not found: $binary_path" >&2
    exit 1
fi

mkdir -p "$output_dir"

root_dir="$(cd "$(dirname "$0")/.." && pwd)"
output_dir="$(cd "$output_dir" && pwd)"
package_name="thane_${version}_${target_os}_${target_arch}"
stage_dir="$(mktemp -d "${TMPDIR:-/tmp}/thane-release.XXXXXX")"
package_dir="$stage_dir/$package_name"

cleanup() {
    rm -rf "$stage_dir"
}
trap cleanup EXIT

mkdir -p "$package_dir/examples" "$package_dir/init"

install -m 755 "$binary_path" "$package_dir/thane"
install -m 644 "$root_dir/README.md" "$package_dir/README.md"
install -m 644 "$root_dir/LICENSE" "$package_dir/LICENSE"
cp -R "$root_dir/examples/." "$package_dir/examples/"
cp -R "$root_dir/init/." "$package_dir/init/"

case "$target_os" in
    darwin)
        archive_path="$output_dir/${package_name}.zip"
        (
            cd "$stage_dir"
            COPYFILE_DISABLE=1 zip -X -rq "$archive_path" "$package_name"
        )
        ;;
    *)
        archive_path="$output_dir/${package_name}.tar.gz"
        tar -C "$stage_dir" -czf "$archive_path" "$package_name"
        ;;
esac

printf '%s\n' "$archive_path"
