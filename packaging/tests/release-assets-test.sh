#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

version="1.2.3"
expected_assets=(
  "cf-optimizer-${version}-windows-amd64-setup.exe"
  "cf-optimizer-${version}-windows-arm64-setup.exe"
  "cf-optimizer-${version}-linux-amd64.tar.gz"
  "cf-optimizer-${version}-linux-arm64.tar.gz"
  "cf-optimizer-${version}-darwin-amd64.dmg"
  "cf-optimizer-${version}-darwin-arm64.dmg"
)

for asset_name in "${expected_assets[@]}"; do
  printf 'package\n' > "$test_root/$asset_name"
done
bash "$repo_root/packaging/verify-release-assets.sh" "v$version" "$test_root"

printf 'unexpected\n' > "$test_root/cf-optimizer-${version}-linux-amd64.rpm"
if bash "$repo_root/packaging/verify-release-assets.sh" "$version" "$test_root" >/dev/null 2>&1; then
  echo "asset validation accepted an extra package" >&2
  exit 1
fi
rm "$test_root/cf-optimizer-${version}-linux-amd64.rpm"

rm "$test_root/${expected_assets[0]}"
if bash "$repo_root/packaging/verify-release-assets.sh" "$version" "$test_root" >/dev/null 2>&1; then
  echo "asset validation accepted a missing package" >&2
  exit 1
fi

downloads="$(bash "$repo_root/packaging/release-downloads.sh" "v$version" "owner/repository")"
for asset_name in "${expected_assets[@]}"; do
  [[ "$(grep -Fo "$asset_name" <<<"$downloads" | wc -l)" -eq 1 ]] || {
    echo "download table does not contain exactly one link for $asset_name" >&2
    exit 1
  }
done
[[ "$(grep -Fo SHA256SUMS <<<"$downloads" | wc -l)" -eq 2 ]]
