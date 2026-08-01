#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "请使用 root 权限运行：sudo ./install.sh" >&2
  echo "Run this installer as root: sudo ./install.sh" >&2
  exit 1
fi

bundle_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# find_package 严格要求归档中每种格式只有一个包，避免误装同目录下的其他版本。
find_package() {
  local extension="$1"
  local matches=("$bundle_dir"/cf-optimizer-*-linux-*."$extension")
  if [[ "${#matches[@]}" -ne 1 || ! -f "${matches[0]}" ]]; then
    echo "安装归档中缺少唯一的 .$extension 包。" >&2
    echo "The bundle does not contain exactly one .$extension package." >&2
    exit 1
  fi
  printf '%s\n' "${matches[0]}"
}

if command -v apt-get >/dev/null 2>&1 && command -v dpkg >/dev/null 2>&1; then
  package_path="$(find_package deb)"
  exec apt-get install -y "$package_path"
fi

if command -v dnf >/dev/null 2>&1 && command -v rpm >/dev/null 2>&1; then
  package_path="$(find_package rpm)"
  exec dnf install -y "$package_path"
fi

if command -v yum >/dev/null 2>&1 && command -v rpm >/dev/null 2>&1; then
  package_path="$(find_package rpm)"
  exec yum install -y "$package_path"
fi

echo "未检测到受支持的包管理器（apt-get、dnf 或 yum）。" >&2
echo "No supported package manager was found (apt-get, dnf, or yum)." >&2
exit 1
