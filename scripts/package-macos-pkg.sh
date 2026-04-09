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
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

if [ ! -f "$binary_path" ]; then
    echo "binary not found: $binary_path" >&2
    exit 1
fi

if [ ! -f "$repo_root/LICENSE" ]; then
    echo "license file not found: $repo_root/LICENSE" >&2
    exit 1
fi

case "$target_arch" in
    amd64)
        host_arch="x86_64"
        arch_label="Intel"
        ;;
    arm64)
        host_arch="arm64"
        arch_label="Apple Silicon"
        ;;
    *)
        echo "unsupported macOS target architecture: $target_arch" >&2
        exit 1
        ;;
esac

mkdir -p "$output_dir"

output_dir="$(cd "$output_dir" && pwd)"
package_name="thane_${version}_darwin_${target_arch}.pkg"
stage_dir="$(mktemp -d "${TMPDIR:-/tmp}/thane-pkg.XXXXXX")"
payload_root="$stage_dir/root"
component_pkg="$stage_dir/thane-component.pkg"
distribution_path="$stage_dir/Distribution.xml"
requirements_plist="$stage_dir/product-requirements.plist"
resources_root="$stage_dir/Resources"
localized_resources="$resources_root/English.lproj"

cleanup() {
    rm -rf "$stage_dir"
}
trap cleanup EXIT

mkdir -p "$payload_root/usr/local/bin" "$localized_resources"
install -m 755 "$binary_path" "$payload_root/usr/local/bin/thane"
if command -v xattr >/dev/null 2>&1; then
    xattr -cr "$payload_root"
fi

artifact_path="$output_dir/$package_name"

cat >"$requirements_plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>arch</key>
    <array>
        <string>${host_arch}</string>
    </array>
    <key>home</key>
    <false/>
</dict>
</plist>
EOF

cat >"$localized_resources/welcome.txt" <<EOF
Welcome to Thane

This installer places the thane command-line binary in /usr/local/bin so it
is available to local users from a normal shell.
EOF

cat >"$localized_resources/readme.txt" <<EOF
Thane ${version} for ${arch_label}

This package installs thane to /usr/local/bin/thane and is intended for
local-system installation.

After installation, run:

  thane version

to confirm the installed build.
EOF

cp "$repo_root/LICENSE" "$localized_resources/LICENSE.txt"

pkgbuild_args=(
    pkgbuild
    --root "$payload_root"
    --identifier "info.nugget.thane.component"
    --version "$version"
    --install-location "/"
    --ownership recommended
    --quiet
    "$component_pkg"
)

# Keep stdout reserved for the final artifact path so release recipes can
# safely capture this script's result with command substitution.
COPYFILE_DISABLE=1 "${pkgbuild_args[@]}" >&2

productbuild --synthesize \
    --product "$requirements_plist" \
    --package "$component_pkg" \
    "$distribution_path" >&2

# Add first-party installer metadata so the final product archive is richer to
# inspect and clearly constrained to the system domain on the intended CPU
# family.
distribution_tmp="${distribution_path}.tmp"
awk '
    /<installer-gui-script minSpecVersion="1">/ {
        print
        print "    <title>Thane Command-Line Agent</title>"
        print "    <welcome file=\"welcome.txt\" mime-type=\"text/plain\"/>"
        print "    <readme file=\"readme.txt\" mime-type=\"text/plain\"/>"
        print "    <license file=\"LICENSE.txt\" mime-type=\"text/plain\"/>"
        print "    <domains enable_anywhere=\"false\" enable_currentUserHome=\"false\" enable_localSystem=\"true\"/>"
        next
    }
    { print }
' "$distribution_path" >"$distribution_tmp"
mv "$distribution_tmp" "$distribution_path"

productbuild_args=(
    productbuild
    --distribution "$distribution_path"
    --package-path "$stage_dir"
    --resources "$resources_root"
    --identifier "info.nugget.thane"
    --version "$version"
    --quiet
)
if [ -n "$installer_identity" ] && [ "$installer_identity" != "-" ]; then
    productbuild_args+=(--sign "$installer_identity")
fi
productbuild_args+=("$artifact_path")

COPYFILE_DISABLE=1 "${productbuild_args[@]}" >&2

if [ -n "$installer_identity" ] && [ "$installer_identity" != "-" ]; then
    pkgutil --check-signature "$artifact_path" >&2
fi

printf '%s\n' "$artifact_path"
