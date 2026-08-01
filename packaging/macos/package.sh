#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: package.sh VERSION amd64|arm64 STAGE_DIR APP_PATH OUTPUT_DIR" >&2
  exit 2
fi

version="${1#v}"
arch="$2"
stage_dir="$(cd "$3" && pwd)"
app_path="$(cd "$(dirname "$4")" && pwd)/$(basename "$4")"
output_dir="$5"

case "$arch" in
  amd64|arm64) ;;
  *) echo "unsupported macOS architecture: $arch" >&2; exit 2 ;;
esac
for binary in cf-optimizer cf-optimizerd; do
  [[ -x "$stage_dir/$binary" ]] || { echo "missing staged binary: $binary" >&2; exit 1; }
done
[[ -d "$app_path" ]] || { echo "missing Wails application: $app_path" >&2; exit 1; }

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
root_dir="$work_dir/root"
scripts_dir="$work_dir/scripts"
mkdir -p "$root_dir/Applications" "$root_dir/usr/local/bin" \
  "$root_dir/usr/local/share/cf-optimizer" "$scripts_dir" "$output_dir"

ditto "$app_path" "$root_dir/Applications/CF Optimizer.app"
install -m 0755 "$stage_dir/cf-optimizer" "$root_dir/usr/local/bin/cf-optimizer"
install -m 0755 "$stage_dir/cf-optimizerd" "$root_dir/usr/local/bin/cf-optimizerd"
install -m 0644 "$repo_root/config.example.yaml" "$root_dir/usr/local/share/cf-optimizer/config.example.yaml"
install -m 0755 "$repo_root/packaging/macos/uninstall.sh" "$root_dir/usr/local/share/cf-optimizer/uninstall.sh"
install -m 0755 "$repo_root/packaging/macos/scripts/preinstall" "$scripts_dir/preinstall"
install -m 0755 "$repo_root/packaging/macos/scripts/postinstall" "$scripts_dir/postinstall"

if [[ -n "${MACOS_APP_SIGN_IDENTITY:-}" ]]; then
  codesign --force --options runtime --timestamp --sign "$MACOS_APP_SIGN_IDENTITY" \
    "$root_dir/usr/local/bin/cf-optimizer" "$root_dir/usr/local/bin/cf-optimizerd"
  codesign --force --deep --options runtime --timestamp \
    --entitlements "$repo_root/packaging/macos/entitlements.plist" \
    --sign "$MACOS_APP_SIGN_IDENTITY" "$root_dir/Applications/CF Optimizer.app"
fi

package_path="$work_dir/cf-optimizer-${version}-darwin-${arch}.pkg"
pkgbuild_args=(--root "$root_dir" --scripts "$scripts_dir" --identifier com.cfoptimizer.package --version "$version")
if [[ -n "${MACOS_INSTALLER_SIGN_IDENTITY:-}" ]]; then
  pkgbuild_args+=(--sign "$MACOS_INSTALLER_SIGN_IDENTITY")
fi
pkgbuild "${pkgbuild_args[@]}" "$package_path"

if [[ -n "${MACOS_NOTARY_KEY:-}" && -n "${MACOS_NOTARY_KEY_ID:-}" && -n "${MACOS_NOTARY_ISSUER:-}" ]]; then
  xcrun notarytool submit "$package_path" --wait \
    --key "$MACOS_NOTARY_KEY" --key-id "$MACOS_NOTARY_KEY_ID" --issuer "$MACOS_NOTARY_ISSUER"
  xcrun stapler staple "$package_path"
fi

dmg_source="$work_dir/dmg"
mkdir -p "$dmg_source"
cp "$package_path" "$dmg_source/"
hdiutil create -quiet -volname "CF Optimizer ${version}" -srcfolder "$dmg_source" \
  -ov -format UDZO "$output_dir/cf-optimizer-${version}-darwin-${arch}.dmg"
