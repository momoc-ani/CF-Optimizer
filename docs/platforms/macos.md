# macOS 使用指南 / macOS Guide

[中文](#中文) | [English](#english) | [返回 README](../../README.md)

## 中文

### 1. 适用范围

本指南适用于 macOS 11 或更高版本的 Intel amd64 与 Apple Silicon arm64 主机。安装 PKG、注册 LaunchDaemon、修改配置和管理系统路由需要管理员权限；Wails 桌面 UI 以普通用户权限运行。

### 2. 下载并校验

在终端查看架构：

```bash
uname -m
```

- `arm64`：下载 `cf-optimizer-<version>-darwin-arm64.dmg`。
- `x86_64`：下载 `cf-optimizer-<version>-darwin-amd64.dmg`。

每个架构只发布一个 DMG；PKG 位于 DMG 内部，不作为单独下载项发布。

同时下载 `SHA256SUMS`，计算 DMG 哈希并与清单中同名文件比较：

```bash
asset='cf-optimizer-<version>-darwin-arm64.dmg'
shasum -a 256 "$asset"
grep "  $asset\$" SHA256SUMS
```

两个值不一致时不要打开 DMG。正式分发应使用 Developer ID 签名和 Apple 公证；未配置证书的 CI 包可能被 Gatekeeper 拦截。

### 3. 安装

1. 打开与当前架构匹配的 DMG。
2. 运行其中的 PKG，并按安装器提示授权。
3. PKG 安装桌面程序与 CLI，创建 `/Library/Application Support/CF Optimizer/config.yaml`，注册并启动 `com.cfoptimizer.daemon` LaunchDaemon。
4. 从“应用程序”打开 CF Optimizer，或运行：

   ```bash
   open -a 'CF Optimizer'
   ```

若可信但未签名的 CI 包被 Gatekeeper 阻止，请在“系统设置 -> 隐私与安全性”中核对文件来源后选择“仍要打开”。不要全局关闭 Gatekeeper。

### 4. 路径与权限

| 内容 | 默认位置 |
|---|---|
| 桌面程序 | `/Applications/CF Optimizer.app` |
| CLI | `/usr/local/bin/cf-optimizer` |
| 后台服务 | `/usr/local/bin/cf-optimizerd` |
| 配置 | `/Library/Application Support/CF Optimizer/config.yaml` |
| 状态、历史、日志和路由事务 | `/Library/Application Support/CF Optimizer` |
| IPC | `/Library/Application Support/CF Optimizer/daemon.sock` |
| LaunchDaemon | `/Library/LaunchDaemons/com.cfoptimizer.daemon.plist` |
| 卸载脚本 | `/usr/local/share/cf-optimizer/uninstall.sh` |

配置文件权限在首次安装时设置为 `0600`。编辑配置、控制 LaunchDaemon 或运行卸载脚本时使用 `sudo`；不要以 root 身份长期启动桌面 UI。

### 5. 三步开始使用

普通用户只需完成三步：

1. 下载对应架构的 DMG，并运行其中的 PKG 完成安装。
2. 从“应用程序”打开 CF Optimizer，等待总览显示后台已连接；程序会自动执行只读物理出口预检。
3. 点击“一键优选”，在一个确认框中核对接口、网关和影响范围，然后选择“仅本次应用”或“以后自动维护”并开始。

确认前不会修改路由、代理策略或持续维护配置。后台会依次更新网段、测速、应用并验证策略；验证失败时回滚。界面只会显示“已验证”“仅测速完成”“部分完成”或“已回滚”，不会把配置写入当作直连证据。

如果自动预检无法确定可信接口或网关，确认框只提供“仅测速”和“高级设置”。此时再到设置页填写物理接口/网关，并在网络路由页运行诊断；CLI、手工 SHA-256 校验和配置编辑均属于高级路径。

VPN Network Extension、Kill Switch 或企业配置描述文件可能阻止物理出口。没有实际接口、网关和连接证据时，不应声称流量已经直连。

#### 域名加速与代理适配

- `acceleration.manual_domains` 默认为空。后台按配置顺序优先消费测速排名池，每个手动域名独占一个通过其 SNI/Host 预检的优选 IP。
- 未使用代理内核时，可启用 Hosts 适配器写入 `/etc/hosts`；系统主机路由仍负责把优选 IP 指向已验证的物理网关。
- Mihomo/Clash 的控制端口会从 `lsof` 的本机监听进程自动探测，并在找到活动配置后热加载精确 `hosts` 与 `DIRECT` 规则。sing-box 和 Xray 使用显式配置的受管 JSON 片段与安全重载命令；无法识别的代理可使用 Generic Route 或 External JSON-RPC。
- 自动发现只观察 Mihomo 活动连接。仅当域名加速、自动发现和自动应用三项同时开启时，已验证自动域名才继续消费手动域名后的剩余优选 IP；池耗尽时保持未分配。订阅刷新、节点切换、失败、清理和卸载均通过累计收据逆序恢复。

### 6. 桌面界面与托盘

- 从“应用程序”启动后，可在八个页面中管理测速、适配器、路由、网段、历史、日志和设置。
- 关闭主窗口会隐藏到菜单栏托盘，不停止 LaunchDaemon 或当前任务。
- 托盘菜单可重新打开窗口；“退出界面”仅退出普通权限 UI。
- 后台服务继续周期运行；再次打开应用会通过 Unix Domain Socket 恢复当前状态。

### 7. 常用 CLI

```bash
cfopt=/usr/local/bin/cf-optimizer
sudo "$cfopt" service-status       # LaunchDaemon 状态
sudo "$cfopt" status               # 后台服务状态
sudo "$cfopt" benchmark            # 仅测速，不应用策略
sudo "$cfopt" optimize             # 测速并应用已配置、已验证策略
sudo "$cfopt" cancel               # 取消当前任务
sudo "$cfopt" ranges get           # 查看网段缓存
sudo "$cfopt" ranges update        # 刷新网段
sudo "$cfopt" proxy detect         # 检测代理适配器
sudo "$cfopt" history              # 查看历史摘要
sudo "$cfopt" logs --lines 200     # 查看结构化日志
sudo "$cfopt" config show          # 查看规范化配置
sudo "$cfopt" config validate      # 验证配置
```

全局参数必须放在命令前。路径包含空格时必须加引号：

```bash
sudo /usr/local/bin/cf-optimizer \
  --config '/Library/Application Support/CF Optimizer/config.yaml' \
  --json status
```

### 8. 服务管理

```bash
sudo /usr/local/bin/cf-optimizer stop
sudo /usr/local/bin/cf-optimizer start
sudo /usr/local/bin/cf-optimizer service-status
```

要在服务停止后恢复受管路由和代理配置：

```bash
sudo /usr/local/bin/cf-optimizer stop
sudo /usr/local/bin/cf-optimizer cleanup
```

`cleanup` 不删除配置、日志和历史。如果受管文件已被其他程序修改，程序会拒绝覆盖并要求人工核对。

### 9. 升级与卸载

升级时打开新版本同架构 DMG 并运行 PKG。预安装脚本会停止旧 LaunchDaemon，安装后脚本会重新启动服务；配置、状态和历史保持不变。升级前建议备份 `/Library/Application Support/CF Optimizer`。

卸载使用随包安装的脚本：

```bash
sudo /usr/local/share/cf-optimizer/uninstall.sh
```

脚本先停止服务并回滚持久化策略，再移除 LaunchDaemon、CLI、后台二进制和应用程序。配置、日志与历史默认保留。只有确认策略已恢复且不再需要数据后，才手动删除 `/Library/Application Support/CF Optimizer`。

### 10. 排障

```bash
sudo launchctl print system/com.cfoptimizer.daemon
sudo /usr/local/bin/cf-optimizer service-status
sudo /usr/local/bin/cf-optimizer logs --lines 200
tail -n 100 /var/log/cf-optimizer.stderr.log
```

- LaunchDaemon 不存在：重新运行 PKG，或确认安装没有被权限/安全策略中断。
- 服务未运行：执行 `sudo /usr/local/bin/cf-optimizer start` 并检查 stderr 与应用日志。
- UI 无法连接：确认 `daemon.sock` 存在且服务状态正常，不要用 root 启动 UI 来规避权限问题。
- Gatekeeper 阻止应用：核对哈希和来源后使用“隐私与安全性”中的单次批准，不要禁用系统保护。
- 配置无效：恢复 `.bak` 文件，或根据 `config validate` 输出修正 YAML。
- 路由证据错误：关闭路由管理，停止服务并运行 `cleanup`，检查网络服务名、网关、VPN Network Extension 和 Kill Switch。

---

## English

### 1. Scope

This guide covers Intel amd64 and Apple Silicon arm64 hosts running macOS 11 or later. Installing the PKG, registering the LaunchDaemon, editing system configuration, and managing routes require administrator privileges. The Wails desktop UI runs as the normal user.

### 2. Download and verify

Check the architecture in Terminal:

```bash
uname -m
```

- `arm64`: download `cf-optimizer-<version>-darwin-arm64.dmg`.
- `x86_64`: download `cf-optimizer-<version>-darwin-amd64.dmg`.

Each architecture publishes one DMG. Its PKG remains inside the disk image and is not a separate release download.

Download `SHA256SUMS`, calculate the DMG hash, and compare it with the entry for the same filename:

```bash
asset='cf-optimizer-<version>-darwin-arm64.dmg'
shasum -a 256 "$asset"
grep "  $asset\$" SHA256SUMS
```

Do not open the DMG when the values differ. Production releases should use Developer ID signing and Apple notarization. Gatekeeper may block unsigned CI packages.

### 3. Install

1. Open the DMG matching the current architecture.
2. Run the enclosed PKG and authorize the installer.
3. The PKG installs the desktop application and CLI, creates `/Library/Application Support/CF Optimizer/config.yaml`, and registers and starts the `com.cfoptimizer.daemon` LaunchDaemon.
4. Open CF Optimizer from Applications, or run:

   ```bash
   open -a 'CF Optimizer'
   ```

If Gatekeeper blocks a trusted but unsigned CI package, verify its source and use Open Anyway under System Settings > Privacy & Security. Do not disable Gatekeeper globally.

### 4. Paths and permissions

| Content | Default path |
|---|---|
| Desktop application | `/Applications/CF Optimizer.app` |
| CLI | `/usr/local/bin/cf-optimizer` |
| Background service | `/usr/local/bin/cf-optimizerd` |
| Configuration | `/Library/Application Support/CF Optimizer/config.yaml` |
| State, history, logs, and route journal | `/Library/Application Support/CF Optimizer` |
| IPC | `/Library/Application Support/CF Optimizer/daemon.sock` |
| LaunchDaemon | `/Library/LaunchDaemons/com.cfoptimizer.daemon.plist` |
| Uninstaller | `/usr/local/share/cf-optimizer/uninstall.sh` |

The initial configuration is created with mode `0600`. Use `sudo` to edit it, control the LaunchDaemon, or run the uninstaller. Do not run the desktop UI as root.

### 5. Start in three steps

The normal workflow has three steps:

1. Download the DMG for the Mac architecture and run its PKG installer.
2. Open CF Optimizer from Applications and wait for the Overview to show a connected service. The application performs a read-only physical-egress preflight automatically.
3. Select One-click Optimize, review the interface, gateway, and effects in one confirmation dialog, then choose Apply once or Maintain automatically and start.

Before confirmation, the application does not change routes, proxy policy, or persistent maintenance settings. The service refreshes ranges, benchmarks candidates, applies policy, and verifies it in sequence, rolling back when verification fails. The UI reports only Verified, Benchmark only, Partially completed, or Rolled back; a successful write is never treated as direct-traffic proof.

If preflight cannot identify a trusted interface or gateway, the dialog offers only Benchmark and Advanced settings. Enter manual interface/gateway overrides in Settings and use Routes diagnostics only in that fallback path. CLI use, manual SHA-256 verification, and configuration editing are advanced operations.

A VPN Network Extension, Kill Switch, or managed profile may still block physical egress. Do not claim direct traffic without effective interface, gateway, and connection evidence.

#### Domain acceleration and proxy adapters

- `acceleration.manual_domains` is empty by default. The daemon consumes the ranked pool in configuration order, assigning each manual hostname one exclusive optimized IP that passes its SNI and Host preflight.
- Without a proxy core, enable the Hosts adapter to manage `/etc/hosts`; the verified physical gateway remains responsible for the selected IP host route.
- Mihomo/Clash controller ports are discovered from local `lsof` listeners, and exact `hosts` plus `DIRECT` rules are hot-reloaded after the active configuration is found. sing-box and Xray use explicitly configured managed JSON fragments and reload commands. Unknown clients can use Generic Route or versioned External JSON-RPC.
- Automatic discovery observes Mihomo connections only. Verified automatic hostnames consume the pool left after manual assignments only when acceleration, automatic discovery, and automatic apply are all enabled; exhausted pools leave later hostnames unassigned. Subscription refresh, node switches, failures, cleanup, and uninstall restore cumulative receipts in reverse order.

### 6. Desktop and tray

- Launch the application from Applications to manage benchmark, adapters, routes, ranges, history, logs, and settings across eight views.
- Closing the main window hides it to the menu bar tray without stopping the LaunchDaemon or active task.
- Use the tray menu to restore the window. Quit UI exits only the unprivileged desktop process.
- The service continues on schedule, and reopening the application restores current state over the Unix Domain Socket.

### 7. Common CLI commands

```bash
cfopt=/usr/local/bin/cf-optimizer
sudo "$cfopt" service-status       # LaunchDaemon state
sudo "$cfopt" status               # Daemon runtime state
sudo "$cfopt" benchmark            # Benchmark without applying policy
sudo "$cfopt" optimize             # Benchmark and apply configured, verified policy
sudo "$cfopt" cancel               # Cancel the active task
sudo "$cfopt" ranges get           # Inspect range cache
sudo "$cfopt" ranges update        # Refresh ranges
sudo "$cfopt" proxy detect         # Detect proxy adapters
sudo "$cfopt" history              # Show history summaries
sudo "$cfopt" logs --lines 200     # Show structured logs
sudo "$cfopt" config show          # Show normalized configuration
sudo "$cfopt" config validate      # Validate configuration
```

Global options must precede the command. Quote paths containing spaces:

```bash
sudo /usr/local/bin/cf-optimizer \
  --config '/Library/Application Support/CF Optimizer/config.yaml' \
  --json status
```

### 8. Service management

```bash
sudo /usr/local/bin/cf-optimizer stop
sudo /usr/local/bin/cf-optimizer start
sudo /usr/local/bin/cf-optimizer service-status
```

To restore managed routes and proxy policy while the service is stopped:

```bash
sudo /usr/local/bin/cf-optimizer stop
sudo /usr/local/bin/cf-optimizer cleanup
```

`cleanup` does not delete configuration, logs, or history. It refuses to overwrite a managed file changed by another program and reports the conflict for manual review.

### 9. Upgrade and remove

Open the newer DMG for the same architecture and run its PKG. The preinstall script stops the old LaunchDaemon, and postinstall restarts it while preserving configuration, state, and history. Back up `/Library/Application Support/CF Optimizer` before a major upgrade.

Run the installed uninstaller:

```bash
sudo /usr/local/share/cf-optimizer/uninstall.sh
```

It stops the service, rolls back persisted policy, and removes the LaunchDaemon, CLI, daemon binary, and application. Configuration, logs, and history remain by default. Delete `/Library/Application Support/CF Optimizer` manually only after confirming policy restoration and that the data is no longer needed.

### 10. Troubleshooting

```bash
sudo launchctl print system/com.cfoptimizer.daemon
sudo /usr/local/bin/cf-optimizer service-status
sudo /usr/local/bin/cf-optimizer logs --lines 200
tail -n 100 /var/log/cf-optimizer.stderr.log
```

- LaunchDaemon missing: rerun the PKG and confirm installation was not interrupted by permissions or security policy.
- Service stopped: run `sudo /usr/local/bin/cf-optimizer start`, then inspect stderr and application logs.
- UI cannot connect: confirm `daemon.sock` exists and the service is healthy. Do not run the UI as root to bypass permissions.
- Gatekeeper blocks the application: verify checksum and source, then use the one-time approval under Privacy & Security instead of disabling protection.
- Invalid configuration: restore the `.bak` file or correct the YAML field reported by `config validate`.
- Unexpected route evidence: disable route management, stop the service, run `cleanup`, and inspect the network service name, gateway, VPN Network Extension, and Kill Switch.
