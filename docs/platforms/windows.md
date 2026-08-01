# Windows 使用指南 / Windows Guide

[中文](#中文) | [English](#english) | [返回 README](../../README.md)

## 中文

### 1. 适用范围

本指南适用于 Windows 10/11 的 amd64 与 arm64 主机。WSL 不是 Windows 网络服务的部署位置；即使项目源码位于 WSL，也应在 Windows 主机安装 Windows 版本，才能正确使用 Windows Service、Named Pipe、物理网卡和主机路由。

运行要求：

- 具备管理员权限的安装账户。
- Microsoft Edge WebView2 Runtime。安装器检测到缺失时会运行 Microsoft Evergreen Bootstrapper。
- 若要管理路由或 Windows Hosts，后台服务必须以安装器配置的系统服务运行；桌面 UI 始终使用普通用户权限。

### 2. 下载并校验安装包

在 PowerShell 中查看系统架构：

```powershell
$env:PROCESSOR_ARCHITECTURE
```

- `AMD64`：下载 `cf-optimizer-<version>-windows-amd64-setup.exe`。
- `ARM64`：下载 `cf-optimizer-<version>-windows-arm64-setup.exe`。

每个架构只发布一个完整 EXE 安装器，不需要额外下载后台服务或桌面程序。

同时下载发布页中的 `SHA256SUMS`。在安装包所在目录计算哈希并与清单中同名文件比较：

```powershell
$installer = ".\cf-optimizer-<version>-windows-amd64-setup.exe"
(Get-FileHash $installer -Algorithm SHA256).Hash.ToLower()
Get-Content .\SHA256SUMS | Select-String ([IO.Path]::GetFileName($installer))
```

两个值不一致时不要运行安装包。未配置 Authenticode 的 CI 构建可能显示未知发布者；正式发行包应检查数字签名后再继续。

### 3. 安装

1. 右键安装包并选择“以管理员身份运行”。
2. 按向导选择安装目录和可选桌面快捷方式。
3. 安装器按需安装 WebView2，创建 `%ProgramData%\CF Optimizer\config.yaml`，注册并启动 `CFOptimizer` 服务。
4. 安装完成后从开始菜单启动 CF Optimizer。

默认安装目录是 `%ProgramFiles%\CF Optimizer`。安装器不会把 CLI 加入全局 `PATH`，PowerShell 示例因此使用完整路径：

```powershell
$cfopt = Join-Path $env:ProgramFiles "CF Optimizer\cf-optimizer.exe"
& $cfopt version
```

### 4. 路径与权限

| 内容 | 默认位置 |
|---|---|
| CLI | `%ProgramFiles%\CF Optimizer\cf-optimizer.exe` |
| 后台服务 | `%ProgramFiles%\CF Optimizer\cf-optimizerd.exe` |
| 桌面程序 | `%ProgramFiles%\CF Optimizer\cf-optimizer-ui.exe` |
| 配置 | `%ProgramData%\CF Optimizer\config.yaml` |
| 状态、历史、日志和路由事务 | `%ProgramData%\CF Optimizer` |
| IPC | `\\.\pipe\cf-optimizer-v1` |
| 可选 Hosts 文件 | `%SystemRoot%\System32\drivers\etc\hosts` |

查看状态和运行普通诊断通常不需要提升 UI 权限。编辑配置、控制服务或手动清理系统目录时，请使用管理员 PowerShell。

### 5. 三步开始使用

普通用户只需完成三步：

1. 下载并运行对应架构的 Windows 安装包。
2. 从开始菜单打开 CF Optimizer，等待总览显示后台已连接；程序会自动执行只读物理出口预检。
3. 点击“一键优选”，在一个确认框中核对接口、网关和影响范围，然后选择“仅本次应用”或“以后自动维护”并开始。

确认前不会修改路由、Hosts、代理策略或持续维护配置。后台会依次更新网段、测速、应用并验证策略；验证失败时回滚。界面只会显示“已验证”“仅测速完成”“部分完成”或“已回滚”，不会把配置写入当作直连证据。

如果自动预检无法确定可信接口或网关，确认框只提供“仅测速”和“高级设置”。此时再到设置页填写物理接口/网关，并在网络路由页运行诊断；CLI、手工 SHA-256 校验和 YAML 编辑均属于高级路径。

VPN Kill Switch、企业策略或代理内核仍可能阻止物理出口。没有实际接口、网关和连接证据时，不应声称流量已经直连。

### 6. 桌面界面与托盘

- 桌面程序包含总览、测速优选、代理适配、网络路由、网段管理、历史、日志诊断和设置八个页面。
- 点击窗口关闭按钮会隐藏到系统托盘，不会停止 `CFOptimizer` 服务或取消正在运行的任务。
- 从托盘选择“打开 CF Optimizer”可恢复窗口。
- 从托盘选择“退出界面”只退出普通权限 UI，后台服务继续运行；再次从开始菜单启动即可恢复状态。

### 7. 常用 CLI

```powershell
$cfopt = Join-Path $env:ProgramFiles "CF Optimizer\cf-optimizer.exe"
& $cfopt service-status       # Windows Service 状态
& $cfopt status               # 后台服务运行状态
& $cfopt benchmark            # 仅测速，不应用策略
& $cfopt optimize             # 测速并应用已配置、已验证策略
& $cfopt cancel               # 取消当前优选任务
& $cfopt ranges get           # 查看 Cloudflare 网段缓存
& $cfopt ranges update        # 刷新网段
& $cfopt proxy detect         # 检测已配置代理适配器
& $cfopt history              # 查看历史摘要
& $cfopt logs --lines 200     # 查看近期结构化日志
& $cfopt config show          # 查看规范化配置
& $cfopt config validate      # 验证配置
```

全局参数必须放在命令前，例如：

```powershell
& $cfopt --config "$env:ProgramData\CF Optimizer\config.yaml" --json status
```

### 8. 服务管理

在管理员 PowerShell 中运行：

```powershell
& $cfopt stop
& $cfopt start
& $cfopt service-status
```

需要在服务停止后单独恢复受管策略时：

```powershell
& $cfopt stop
& $cfopt cleanup
```

`cleanup` 逆序回滚持久化的受管路由、Hosts 和代理策略，不删除配置、日志或历史。如果受管文件被其他程序修改，清理会拒绝覆盖并返回错误，需先人工核对。

### 9. 升级与卸载

升级时运行同架构的新安装包。安装器会停止现有服务、替换程序文件并重新启动服务，同时保留配置、状态和历史。升级前仍建议备份 `%ProgramData%\CF Optimizer`。

卸载时打开“设置 -> 应用 -> 已安装的应用”，选择 CF Optimizer。卸载器会先停止服务并执行受管策略清理；清理失败时会中止卸载，避免遗留未知网络状态。

默认保留 `%ProgramData%\CF Optimizer`。确认服务已经移除、受管策略已回滚且不再需要历史后，才可手动删除该产品专属目录。

### 10. 排障

服务或 UI 无法连接时：

```powershell
sc.exe query CFOptimizer
& $cfopt service-status
& $cfopt logs --lines 200
```

- `service-status` 显示未安装：重新以管理员身份运行安装器。
- 服务已安装但未运行：执行 `& $cfopt start`，再检查日志。
- UI 白屏或无法启动：在“已安装的应用”中确认 Microsoft Edge WebView2 Runtime 存在并更新系统 WebView2。
- 配置校验失败：恢复备份，或根据 `config validate` 的字段错误修正 YAML。
- 路由诊断不符合预期：关闭 `network.manage_routes`，执行停止与 `cleanup`，并检查 VPN Kill Switch、物理网卡名称和网关。
- 在 WSL 中找不到 Windows Service：这是预期行为，请退出 WSL，在 Windows PowerShell 和 Windows 安装目录中运行命令。

---

## English

### 1. Scope

This guide covers Windows 10/11 hosts on amd64 and arm64. WSL is not the deployment location for the Windows network service. Even when the source tree is stored under WSL, install the Windows package on the Windows host so Windows Service, Named Pipe, physical adapter, and host-route operations use the correct operating system.

Requirements:

- An installation account with Administrator privileges.
- Microsoft Edge WebView2 Runtime. The installer runs the Microsoft Evergreen Bootstrapper when the runtime is missing.
- Route or Windows Hosts management requires the installed system service. The desktop UI always runs without long-lived elevation.

### 2. Download and verify

Check the host architecture in PowerShell:

```powershell
$env:PROCESSOR_ARCHITECTURE
```

- `AMD64`: download `cf-optimizer-<version>-windows-amd64-setup.exe`.
- `ARM64`: download `cf-optimizer-<version>-windows-arm64-setup.exe`.

Each architecture publishes one complete EXE installer; the service and desktop application require no separate downloads.

Download `SHA256SUMS` from the same release. Calculate the installer hash and compare it with the entry for the same filename:

```powershell
$installer = ".\cf-optimizer-<version>-windows-amd64-setup.exe"
(Get-FileHash $installer -Algorithm SHA256).Hash.ToLower()
Get-Content .\SHA256SUMS | Select-String ([IO.Path]::GetFileName($installer))
```

Do not run the installer when the values differ. CI builds may show an unknown publisher when Authenticode credentials are not configured; verify the digital signature for production releases.

### 3. Install

1. Right-click the installer and select Run as administrator.
2. Choose the installation directory and optional desktop shortcut.
3. The installer installs WebView2 when needed, creates `%ProgramData%\CF Optimizer\config.yaml`, and registers and starts the `CFOptimizer` service.
4. Launch CF Optimizer from the Start menu.

The default installation directory is `%ProgramFiles%\CF Optimizer`. The installer does not add the CLI to the global `PATH`, so the examples use its full path:

```powershell
$cfopt = Join-Path $env:ProgramFiles "CF Optimizer\cf-optimizer.exe"
& $cfopt version
```

### 4. Paths and permissions

| Content | Default path |
|---|---|
| CLI | `%ProgramFiles%\CF Optimizer\cf-optimizer.exe` |
| Background service | `%ProgramFiles%\CF Optimizer\cf-optimizerd.exe` |
| Desktop application | `%ProgramFiles%\CF Optimizer\cf-optimizer-ui.exe` |
| Configuration | `%ProgramData%\CF Optimizer\config.yaml` |
| State, history, logs, and route journal | `%ProgramData%\CF Optimizer` |
| IPC | `\\.\pipe\cf-optimizer-v1` |
| Optional Hosts file | `%SystemRoot%\System32\drivers\etc\hosts` |

Reading status and normal diagnostics usually does not require an elevated UI. Use an Administrator PowerShell when editing configuration, controlling the service, or manually removing system data.

### 5. Start in three steps

The normal workflow has three steps:

1. Download and run the Windows installer for the host architecture.
2. Open CF Optimizer from the Start menu and wait for the Overview to show a connected service. The application performs a read-only physical-egress preflight automatically.
3. Select One-click Optimize, review the interface, gateway, and effects in one confirmation dialog, then choose Apply once or Maintain automatically and start.

Before confirmation, the application does not change routes, Hosts, proxy policy, or persistent maintenance settings. The service refreshes ranges, benchmarks candidates, applies policy, and verifies it in sequence, rolling back when verification fails. The UI reports only Verified, Benchmark only, Partially completed, or Rolled back; a successful write is never treated as direct-traffic proof.

If preflight cannot identify a trusted interface or gateway, the dialog offers only Benchmark and Advanced settings. Enter manual interface/gateway overrides in Settings and use Routes diagnostics only in that fallback path. CLI use, manual SHA-256 verification, and YAML editing are advanced operations.

A VPN Kill Switch, enterprise policy, or proxy core may still block physical egress. Do not claim direct traffic without effective interface, gateway, and connection evidence.

### 6. Desktop and tray

- The desktop provides Overview, Benchmark, Proxy, Routes, Ranges, History, Logs/Diagnostics, and Settings views.
- Closing the window hides it to the system tray without stopping `CFOptimizer` or cancelling an active task.
- Select Open CF Optimizer from the tray to restore the window.
- Select Quit UI to exit only the unprivileged UI. The service continues, and launching from the Start menu restores the current state.

### 7. Common CLI commands

```powershell
$cfopt = Join-Path $env:ProgramFiles "CF Optimizer\cf-optimizer.exe"
& $cfopt service-status       # Windows Service state
& $cfopt status               # Daemon runtime state
& $cfopt benchmark            # Benchmark without applying policy
& $cfopt optimize             # Benchmark and apply configured, verified policy
& $cfopt cancel               # Cancel the active optimization
& $cfopt ranges get           # Inspect the Cloudflare range cache
& $cfopt ranges update        # Refresh ranges
& $cfopt proxy detect         # Detect configured proxy adapters
& $cfopt history              # Show history summaries
& $cfopt logs --lines 200     # Show recent structured logs
& $cfopt config show          # Show normalized configuration
& $cfopt config validate      # Validate configuration
```

Global options must precede the command:

```powershell
& $cfopt --config "$env:ProgramData\CF Optimizer\config.yaml" --json status
```

### 8. Service management

Run these commands in an Administrator PowerShell:

```powershell
& $cfopt stop
& $cfopt start
& $cfopt service-status
```

To restore managed policy separately while the service is stopped:

```powershell
& $cfopt stop
& $cfopt cleanup
```

`cleanup` rolls back persisted managed routes, Hosts, and proxy policy in reverse order without deleting configuration, logs, or history. If another program changed a managed file, cleanup refuses to overwrite it and reports an error for manual review.

### 9. Upgrade and remove

Run the newer installer for the same architecture. It stops the existing service, replaces program files, and restarts the service while preserving configuration, state, and history. Back up `%ProgramData%\CF Optimizer` before a major upgrade.

To remove the application, open Settings > Apps > Installed apps and select CF Optimizer. The uninstaller stops the service and cleans managed policy first. It aborts when cleanup fails to avoid leaving an unknown network state.

`%ProgramData%\CF Optimizer` is preserved by default. Remove this product-specific directory manually only after confirming the service is gone, managed policy has been restored, and history is no longer needed.

### 10. Troubleshooting

When the service or UI cannot connect:

```powershell
sc.exe query CFOptimizer
& $cfopt service-status
& $cfopt logs --lines 200
```

- Service not installed: rerun the installer as Administrator.
- Service installed but stopped: run `& $cfopt start`, then inspect logs.
- Blank UI or startup failure: confirm Microsoft Edge WebView2 Runtime is installed under Installed apps and update the runtime.
- Configuration validation failure: restore the backup or correct the YAML field reported by `config validate`.
- Unexpected route diagnostics: disable `network.manage_routes`, stop and run `cleanup`, then inspect the VPN Kill Switch, physical adapter name, and gateway.
- Windows Service missing under WSL: this is expected. Leave WSL and run commands from Windows PowerShell against the Windows installation.
