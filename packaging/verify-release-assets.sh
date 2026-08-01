#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: verify-release-assets.sh VERSION ASSET_DIR" >&2
  exit 2
fi

version="${1#v}"
asset_dir="$2"
[[ -d "$asset_dir" ]] || { echo "asset directory does not exist: $asset_dir" >&2; exit 1; }

expected_assets=(
  "cf-optimizer-${version}-windows-amd64-setup.exe"
  "cf-optimizer-${version}-windows-arm64-setup.exe"
  "cf-optimizer-${version}-linux-amd64.tar.gz"
  "cf-optimizer-${version}-linux-arm64.tar.gz"
  "cf-optimizer-${version}-darwin-amd64.dmg"
  "cf-optimizer-${version}-darwin-arm64.dmg"
)

declare -A expected_by_name=()
for asset_name in "${expected_assets[@]}"; do
  expected_by_name["$asset_name"]=1
  [[ -s "$asset_dir/$asset_name" ]] || {
    echo "missing or empty release asset: $asset_name" >&2
    exit 1
  }
done

mapfile -t actual_assets < <(
  find "$asset_dir" -maxdepth 1 -type f ! -name SHA256SUMS -printf '%f\n' | sort
)
if [[ "${#actual_assets[@]}" -ne "${#expected_assets[@]}" ]]; then
  echo "expected exactly ${#expected_assets[@]} release packages, found ${#actual_assets[@]}" >&2
  printf 'found: %s\n' "${actual_assets[@]}" >&2
  exit 1
fi

for asset_name in "${actual_assets[@]}"; do
  [[ -n "${expected_by_name[$asset_name]:-}" ]] || {
    echo "unexpected release asset: $asset_name" >&2
    exit 1
  }
done
