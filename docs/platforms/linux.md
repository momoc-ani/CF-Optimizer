# Linux 使用指南 / Linux Guide

[中文](#中文) | [English](#english) | [返回 README](../../README.md)

## 中文

### 1. 适用范围

本指南适用于使用 systemd 的 amd64 或 arm64 桌面 Linux。发布包提供 Debian/Ubuntu 的 DEB，以及 Fedora/RHEL 系的 RPM。桌面程序依赖 GTK 3 和 WebKitGTK 4.1；托盘通过桌面会话的 D-Bus StatusNotifier 协议注册。

WSL 不应作为 Windows 主机网络的服务部署环境。要管理 Windows 的物理网卡和路由，请在 Windows 主机安装 Windows 版本。无 systemd 的 Linux 环境不会由安装脚本自动注册服务。

### 2. 下载并校验

查看系统架构：

```bash
uname -m
```

- `x86_64`：下载 `cf-optimizer-<version>-linux-amd64.deb` 或 `.rpm`。
- `aarch64`、`arm64`：下载 `cf-optimizer-<version>-linux-arm64.deb` 或 `.rpm`。

同时下载 `SHA256SUMS`，计算包哈希并与清单中同名文件比较：

```bash
asset='cf-optimizer-<version>-linux-amd64.deb'
sha256sum "$asset"
grep "  $asset\$" SHA256SUMS
```

两个值不一致时不要安装。

### 3. 安装

Debian/Ubuntu：

```bash
sudo apt install ./cf-optimizer-<version>-linux-amd64.deb
```

Fedora/RHEL 系：

```bash
sudo dnf install ./cf-optimizer-<version>-linux-amd64.rpm
```

包安装过程会：

1. 安装 `cf-optimizer`、`cf-optimizerd` 和 `cf-optimizer-ui` 到 `/usr/bin`。
2. 创建 `/etc/cf-optimizer/config.yaml` 并设置权限为 `0600`。
3. 在 systemd 可用时创建、启用并启动 `cf-optimizer.service`。
4. 安装桌面菜单项和应用图标。

从应用菜单启动 CF Optimizer，或运行：

```bash
cf-optimizer-ui
```

不要使用 `sudo cf-optimizer-ui`。需要特权的操作由后台服务完成。

### 4. 路径与权限

| 内容 | 默认位置 |
|---|---|
| CLI | `/usr/bin/cf-optimizer` |
| 后台服务 | `/usr/bin/cf-optimizerd` |
| 桌面程序 | `/usr/bin/cf-optimizer-ui` |
| 配置 | `/etc/cf-optimizer/config.yaml` |
| 状态、历史、日志和路由事务 | `/var/lib/cf-optimizer` |
| IPC | `/var/lib/cf-optimizer/daemon.sock` |
| systemd 单元 | `/etc/systemd/system/cf-optimizer.service` |
| 配置示例 | `/usr/share/cf-optimizer/config.example.yaml` |

普通用户 UI 只通过受权限保护的 Unix Domain Socket 与服务通信。编辑 `/etc` 配置、控制服务和手动清理系统数据时使用 `sudo`。

### 5. 三步开始使用

普通用户只需完成三步：

1. 下载对应架构的 DEB 或 RPM，并使用系统包管理器安装。
2. 从应用菜单打开 CF Optimizer，等待总览显示后台已连接；程序会自动执行只读物理出口预检。
3. 点击“一键优选”，在一个确认框中核对接口、网关和影响范围，然后选择“仅本次应用”或“以后自动维护”并开始。

确认前不会修改路由、代理策略或持续维护配置。后台会依次更新网段、测速、应用并验证策略；验证失败时回滚。界面只会显示“已验证”“仅测速完成”“部分完成”或“已回滚”，不会把规则写入当作直连证据。

如果自动预检无法确定可信接口或网关，确认框只提供“仅测速”和“高级设置”。此时再到设置页填写物理接口/网关，并在网络路由页运行诊断；CLI、手工 SHA-256 校验和配置编辑均属于高级路径。

NetworkManager、策略路由、容器网络、VPN Kill Switch 或代理 TUN 仍可能改变实际出口。没有实际接口、网关和连接证据时，不应声称流量已经直连。

### 6. 桌面界面与托盘

- 桌面程序提供总览、测速优选、代理适配、网络路由、网段管理、历史、日志诊断和设置八个页面。
- 关闭窗口会隐藏到系统托盘，不停止 systemd 服务或当前任务。
- 托盘可恢复窗口；“退出界面”只退出普通权限 UI。
- GNOME 环境若不显示托盘图标，请确认系统启用了 AppIndicator 支持；Ubuntu 通常默认提供，其他发行版可能需要桌面扩展。
- 即使托盘不可见，后台服务仍独立运行；可重新执行 `cf-optimizer-ui` 打开界面。

### 7. 常用 CLI

```bash
sudo cf-optimizer service-status       # systemd 服务状态
sudo cf-optimizer status               # 后台服务状态
sudo cf-optimizer benchmark            # 仅测速，不应用策略
sudo cf-optimizer optimize             # 测速并应用已配置、已验证策略
sudo cf-optimizer cancel               # 取消当前任务
sudo cf-optimizer ranges get           # 查看网段缓存
sudo cf-optimizer ranges update        # 刷新网段
sudo cf-optimizer proxy detect         # 检测代理适配器
sudo cf-optimizer history              # 查看历史摘要
sudo cf-optimizer logs --lines 200     # 查看结构化日志
sudo cf-optimizer config show          # 查看规范化配置
sudo cf-optimizer config validate      # 验证配置
```

全局参数必须放在命令前：

```bash
sudo cf-optimizer --config /etc/cf-optimizer/config.yaml --json status
```

### 8. 服务管理

```bash
sudo cf-optimizer stop
sudo cf-optimizer start
sudo cf-optimizer service-status
```

也可以使用 systemd 查看运行状态和输出：

```bash
sudo systemctl status cf-optimizer.service
sudo journalctl -u cf-optimizer.service -n 100 --no-pager
```

在服务停止后恢复受管路由和代理配置：

```bash
sudo cf-optimizer stop
sudo cf-optimizer cleanup
```

`cleanup` 不删除配置、日志或历史。受管文件被其他程序修改时会拒绝覆盖，避免破坏用户配置。

### 9. 升级与卸载

Debian/Ubuntu 升级：

```bash
sudo apt install ./cf-optimizer-<new-version>-linux-amd64.deb
```

Fedora/RHEL 系升级：

```bash
sudo dnf upgrade ./cf-optimizer-<new-version>-linux-amd64.rpm
```

升级脚本停止旧服务，安装新二进制后重新启动，保留配置、状态和历史。升级前建议备份 `/etc/cf-optimizer` 与 `/var/lib/cf-optimizer`。

卸载：

```bash
# Debian/Ubuntu
sudo apt remove cf-optimizer

# Fedora/RHEL
sudo dnf remove cf-optimizer
```

卸载前脚本会停止服务并清理持久化策略。清理失败时包管理器会中止，以避免遗留未知网络状态。配置和运行数据默认保留；确认策略已恢复且不再需要数据后，才手动删除 `/etc/cf-optimizer` 与 `/var/lib/cf-optimizer`。

### 10. 排障

```bash
sudo systemctl status cf-optimizer.service
sudo journalctl -u cf-optimizer.service -n 200 --no-pager
sudo cf-optimizer service-status
sudo cf-optimizer logs --lines 200
```

- `systemd` 未运行：目标环境不满足正式服务要求。在 WSL 中应改用 Windows 主机版本；其他环境需使用支持 systemd 的桌面发行版。
- 服务未安装：重新安装 DEB/RPM，确认安装脚本没有因权限或缺少 systemd 中断。
- UI 无法启动：确认 GTK 3 和 WebKitGTK 4.1 运行库已安装，并从终端查看错误。
- UI 无法连接：确认 `/var/lib/cf-optimizer/daemon.sock` 存在、服务运行正常且目录权限未被手动修改。
- 托盘不可见：检查桌面环境是否支持 D-Bus StatusNotifier；不要因此用 root 启动 UI。
- 配置无效：恢复 `.bak`，或根据 `config validate` 输出修正 YAML。
- 路由证据错误：关闭路由管理，停止服务并运行 `cleanup`，检查物理网卡、网关、策略路由、VPN 和 TUN。

---

## English

### 1. Scope

This guide covers amd64 and arm64 desktop Linux systems using systemd. Releases provide DEB packages for Debian/Ubuntu and RPM packages for Fedora/RHEL families. The desktop application depends on GTK 3 and WebKitGTK 4.1; its tray registers through the desktop session's D-Bus StatusNotifier protocol.

WSL is not a deployment environment for managing Windows host networking. Install the Windows version on the Windows host to manage its physical adapters and routes. Linux environments without systemd do not receive automatic service registration from the package scripts.

### 2. Download and verify

Check the system architecture:

```bash
uname -m
```

- `x86_64`: download `cf-optimizer-<version>-linux-amd64.deb` or `.rpm`.
- `aarch64` or `arm64`: download `cf-optimizer-<version>-linux-arm64.deb` or `.rpm`.

Download `SHA256SUMS`, calculate the package hash, and compare it with the same filename in the manifest:

```bash
asset='cf-optimizer-<version>-linux-amd64.deb'
sha256sum "$asset"
grep "  $asset\$" SHA256SUMS
```

Do not install the package when the values differ.

### 3. Install

Debian/Ubuntu:

```bash
sudo apt install ./cf-optimizer-<version>-linux-amd64.deb
```

Fedora/RHEL family:

```bash
sudo dnf install ./cf-optimizer-<version>-linux-amd64.rpm
```

Package installation:

1. Installs `cf-optimizer`, `cf-optimizerd`, and `cf-optimizer-ui` under `/usr/bin`.
2. Creates `/etc/cf-optimizer/config.yaml` with mode `0600`.
3. Creates, enables, and starts `cf-optimizer.service` when systemd is available.
4. Installs the desktop menu entry and application icon.

Launch CF Optimizer from the application menu, or run:

```bash
cf-optimizer-ui
```

Do not run `sudo cf-optimizer-ui`. Privileged operations belong to the background service.

### 4. Paths and permissions

| Content | Default path |
|---|---|
| CLI | `/usr/bin/cf-optimizer` |
| Background service | `/usr/bin/cf-optimizerd` |
| Desktop application | `/usr/bin/cf-optimizer-ui` |
| Configuration | `/etc/cf-optimizer/config.yaml` |
| State, history, logs, and route journal | `/var/lib/cf-optimizer` |
| IPC | `/var/lib/cf-optimizer/daemon.sock` |
| systemd unit | `/etc/systemd/system/cf-optimizer.service` |
| Configuration example | `/usr/share/cf-optimizer/config.example.yaml` |

The unprivileged UI only communicates through a permission-protected Unix Domain Socket. Use `sudo` to edit configuration under `/etc`, control the service, or manually remove system data.

### 5. Start in three steps

The normal workflow has three steps:

1. Download the DEB or RPM for the machine architecture and install it with the system package manager.
2. Open CF Optimizer from the application menu and wait for the Overview to show a connected service. The application performs a read-only physical-egress preflight automatically.
3. Select One-click Optimize, review the interface, gateway, and effects in one confirmation dialog, then choose Apply once or Maintain automatically and start.

Before confirmation, the application does not change routes, proxy policy, or persistent maintenance settings. The service refreshes ranges, benchmarks candidates, applies policy, and verifies it in sequence, rolling back when verification fails. The UI reports only Verified, Benchmark only, Partially completed, or Rolled back; a successful rule write is never treated as direct-traffic proof.

If preflight cannot identify a trusted interface or gateway, the dialog offers only Benchmark and Advanced settings. Enter manual interface/gateway overrides in Settings and use Routes diagnostics only in that fallback path. CLI use, manual SHA-256 verification, and configuration editing are advanced operations.

NetworkManager, policy routing, container networking, a VPN Kill Switch, or a proxy TUN may still change the effective egress path. Do not claim direct traffic without effective interface, gateway, and connection evidence.

### 6. Desktop and tray

- The desktop provides Overview, Benchmark, Proxy, Routes, Ranges, History, Logs/Diagnostics, and Settings views.
- Closing the window hides it to the tray without stopping the systemd service or active task.
- The tray restores the window. Quit UI exits only the unprivileged UI.
- If GNOME does not display the tray icon, confirm AppIndicator support is enabled. Ubuntu usually includes it, while other distributions may require a desktop extension.
- The service remains independent when the tray is unavailable. Run `cf-optimizer-ui` again to reopen the interface.

### 7. Common CLI commands

```bash
sudo cf-optimizer service-status       # systemd service state
sudo cf-optimizer status               # Daemon runtime state
sudo cf-optimizer benchmark            # Benchmark without applying policy
sudo cf-optimizer optimize             # Benchmark and apply configured, verified policy
sudo cf-optimizer cancel               # Cancel the active task
sudo cf-optimizer ranges get           # Inspect range cache
sudo cf-optimizer ranges update        # Refresh ranges
sudo cf-optimizer proxy detect         # Detect proxy adapters
sudo cf-optimizer history              # Show history summaries
sudo cf-optimizer logs --lines 200     # Show structured logs
sudo cf-optimizer config show          # Show normalized configuration
sudo cf-optimizer config validate      # Validate configuration
```

Global options must precede the command:

```bash
sudo cf-optimizer --config /etc/cf-optimizer/config.yaml --json status
```

### 8. Service management

```bash
sudo cf-optimizer stop
sudo cf-optimizer start
sudo cf-optimizer service-status
```

Use systemd to inspect runtime state and output:

```bash
sudo systemctl status cf-optimizer.service
sudo journalctl -u cf-optimizer.service -n 100 --no-pager
```

Restore managed routes and proxy policy while the service is stopped:

```bash
sudo cf-optimizer stop
sudo cf-optimizer cleanup
```

`cleanup` does not delete configuration, logs, or history. It refuses to overwrite managed files changed by another program.

### 9. Upgrade and remove

Upgrade on Debian/Ubuntu:

```bash
sudo apt install ./cf-optimizer-<new-version>-linux-amd64.deb
```

Upgrade on Fedora/RHEL family:

```bash
sudo dnf upgrade ./cf-optimizer-<new-version>-linux-amd64.rpm
```

Upgrade scripts stop the old service and restart it after installing new binaries while preserving configuration, state, and history. Back up `/etc/cf-optimizer` and `/var/lib/cf-optimizer` before a major upgrade.

Remove the package:

```bash
# Debian/Ubuntu
sudo apt remove cf-optimizer

# Fedora/RHEL
sudo dnf remove cf-optimizer
```

The removal script stops the service and cleans persisted policy first. Package removal aborts when cleanup fails to avoid leaving an unknown network state. Configuration and runtime data remain by default. Delete `/etc/cf-optimizer` and `/var/lib/cf-optimizer` manually only after confirming policy restoration and that the data is no longer needed.

### 10. Troubleshooting

```bash
sudo systemctl status cf-optimizer.service
sudo journalctl -u cf-optimizer.service -n 200 --no-pager
sudo cf-optimizer service-status
sudo cf-optimizer logs --lines 200
```

- systemd unavailable: the environment does not meet the supported service requirements. Use the Windows host build for WSL, or a desktop distribution with systemd for Linux.
- Service missing: reinstall the DEB/RPM and confirm package scripts were not interrupted by permissions or missing systemd.
- UI startup failure: confirm GTK 3 and WebKitGTK 4.1 runtime libraries are installed, then launch from a terminal to inspect errors.
- UI cannot connect: confirm `/var/lib/cf-optimizer/daemon.sock` exists, the service is healthy, and directory permissions were not changed manually.
- Tray missing: inspect the desktop environment's D-Bus StatusNotifier support. Do not run the UI as root as a workaround.
- Invalid configuration: restore the `.bak` file or correct the YAML field reported by `config validate`.
- Unexpected route evidence: disable route management, stop the service, run `cleanup`, and inspect the physical adapter, gateway, policy routing, VPN, and TUN.
