#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: release-downloads.sh TAG OWNER/REPOSITORY" >&2
  exit 2
fi

tag="$1"
repository="$2"
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "release tag must match v<major>.<minor>.<patch>" >&2
  exit 2
fi
if [[ ! "$repository" =~ ^[^/]+/[^/]+$ ]]; then
  echo "repository must use OWNER/REPOSITORY format" >&2
  exit 2
fi

version="${tag#v}"
download_base="https://github.com/${repository}/releases/download/${tag}"

cat <<EOF
## 平台下载 / Platform downloads

请选择与操作系统和处理器架构匹配的文件。Linux 归档同时支持 Debian/Ubuntu 与 Fedora/RHEL。

Choose the file matching the operating system and processor architecture. Each Linux bundle supports both Debian/Ubuntu and Fedora/RHEL.

| 平台 / Platform | 架构 / Architecture | 下载 / Download |
|---|---|---|
| Windows | amd64 | [EXE](${download_base}/cf-optimizer-${version}-windows-amd64-setup.exe) |
| Windows | arm64 | [EXE](${download_base}/cf-optimizer-${version}-windows-arm64-setup.exe) |
| Linux | amd64 | [TAR.GZ](${download_base}/cf-optimizer-${version}-linux-amd64.tar.gz) |
| Linux | arm64 | [TAR.GZ](${download_base}/cf-optimizer-${version}-linux-arm64.tar.gz) |
| macOS | amd64 | [DMG](${download_base}/cf-optimizer-${version}-darwin-amd64.dmg) |
| macOS | arm64 | [DMG](${download_base}/cf-optimizer-${version}-darwin-arm64.dmg) |

[SHA256SUMS](${download_base}/SHA256SUMS)
EOF
