#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 4 ]]; then
  echo "usage: package.sh VERSION amd64|arm64 STAGE_DIR OUTPUT_DIR" >&2
  exit 2
fi

CFOPT_VERSION="${1#v}"
target_arch="$2"
CFOPT_STAGE="$(cd "$3" && pwd)"
output_dir="$4"
bundle_name="cf-optimizer-${CFOPT_VERSION}-linux-${target_arch}"

case "$target_arch" in
  amd64)
    deb_arch="amd64"
    rpm_arch="x86_64"
    ;;
  arm64)
    deb_arch="arm64"
    rpm_arch="aarch64"
    ;;
  *)
    echo "unsupported Linux architecture: $target_arch" >&2
    exit 2
    ;;
esac

for binary in cf-optimizer cf-optimizerd cf-optimizer-ui; do
  [[ -x "$CFOPT_STAGE/$binary" ]] || { echo "missing staged binary: $binary" >&2; exit 1; }
done
command -v nfpm >/dev/null 2>&1 || { echo "nfpm is required" >&2; exit 1; }

mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
bundle_dir="$work_dir/$bundle_name"
mkdir -p "$bundle_dir"
install -m 0755 packaging/linux/install.sh "$bundle_dir/install.sh"
export CFOPT_VERSION CFOPT_STAGE

NFPM_ARCH="$deb_arch" nfpm package --config packaging/linux/nfpm.yaml --packager deb \
  --target "$bundle_dir/${bundle_name}.deb"
NFPM_ARCH="$rpm_arch" nfpm package --config packaging/linux/nfpm.yaml --packager rpm \
  --target "$bundle_dir/${bundle_name}.rpm"

tar -C "$work_dir" -czf "$output_dir/${bundle_name}.tar.gz" "$bundle_name"
