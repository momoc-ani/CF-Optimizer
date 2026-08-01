#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

stage_dir="$test_root/stage"
fake_bin="$test_root/bin"
mkdir -p "$stage_dir" "$fake_bin"

for binary in cf-optimizer cf-optimizerd cf-optimizer-ui; do
  printf '#!/usr/bin/env bash\nexit 0\n' > "$stage_dir/$binary"
  chmod +x "$stage_dir/$binary"
done

cat > "$fake_bin/nfpm" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

target=""
while [[ $# -gt 0 ]]; do
  if [[ "$1" == "--target" ]]; then
    target="$2"
    break
  fi
  shift
done
[[ -n "$target" ]] || { echo "fake nfpm did not receive --target" >&2; exit 1; }
printf '%s\n' "${NFPM_ARCH:?}" > "$target"
EOF
chmod +x "$fake_bin/nfpm"

# verify_linux_bundle 验证每个架构只公开一个归档，同时保留正确架构的 DEB、RPM 和安装脚本。
verify_linux_bundle() {
  local arch="$1"
  local expected_deb_arch="$2"
  local expected_rpm_arch="$3"
  local output_dir="$test_root/output-$arch"
  local extract_dir="$test_root/extract-$arch"
  local bundle_name="cf-optimizer-1.2.3-linux-$arch"
  local archive_path="$output_dir/$bundle_name.tar.gz"

  mkdir -p "$output_dir" "$extract_dir"
  (
    cd "$repo_root"
    PATH="$fake_bin:$PATH" packaging/linux/package.sh 1.2.3 "$arch" "$stage_dir" "$output_dir"
  )

  mapfile -t outputs < <(find "$output_dir" -maxdepth 1 -type f -printf '%f\n')
  [[ "${#outputs[@]}" -eq 1 && "${outputs[0]}" == "$bundle_name.tar.gz" ]] || {
    echo "unexpected public outputs for linux/$arch: ${outputs[*]}" >&2
    exit 1
  }

  tar -xzf "$archive_path" -C "$extract_dir"
  [[ -x "$extract_dir/$bundle_name/install.sh" ]]
  [[ "$(<"$extract_dir/$bundle_name/$bundle_name.deb")" == "$expected_deb_arch" ]]
  [[ "$(<"$extract_dir/$bundle_name/$bundle_name.rpm")" == "$expected_rpm_arch" ]]
}

verify_linux_bundle amd64 amd64 x86_64
verify_linux_bundle arm64 arm64 aarch64
