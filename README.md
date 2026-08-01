# CF Optimizer

[中文](#中文) | [English](#english)

## 中文

CF Optimizer 是面向 Windows、Linux 和 macOS 的 Cloudflare 节点测速、稳定优选与受控直连管理工具。项目包含独立 Go 测速核心、特权后台服务、诊断 CLI，以及基于 Wails、React 和 Mantine 的普通权限桌面界面。

项目不会仅凭配置写入就宣称直连已经生效。路由和策略应用必须通过实际选路、网关、接口和连接证据验证；若系统、VPN Kill Switch 或代理内核阻止物理出口，程序会报告限制并保留可回滚状态。

### 核心能力

- 更新、严格校验并缓存 Cloudflare 官方 IPv4/IPv6 网段，异常更新自动回退。
- 可复现生成候选 IP，执行 TCP 初筛、TLS 校验和可选的限流 HTTPS 下载复筛。
- 按成功率、延迟、抖动、丢包和吞吐评分，并通过历史平滑、冷却、迟滞与最短保持时间避免频繁切换。
- 在启用后，以事务方式计划、应用、验证和回滚临时网段路由及最终 `/32`、`/128` 主机路由。
- 支持 Generic Route、Mihomo、sing-box、Xray、版本化 External JSON-RPC 与可选 Windows Hosts 适配。
- 通过 Windows Service、systemd 或 LaunchDaemon 运行后台服务；关闭桌面窗口会隐藏到系统托盘，重新打开或退出 UI 都不会停止服务或正在运行的任务。
- 提供总览、测速优选、代理适配、网络路由、网段管理、历史、日志诊断和设置八个桌面页面。

### 进程与权限边界

```text
cf-optimizer-ui  普通权限桌面界面
       |
       v
Wails Bridge -> 白名单 IPC -> cf-optimizerd 特权后台服务
                              ^
                              |
                         cf-optimizer CLI
```

Windows 使用带 ACL 的 Named Pipe，Linux/macOS 使用受权限保护的 Unix Domain Socket。UI 不能执行 shell，也不能直接修改路由、Hosts 或代理配置；IPC 服务端会再次严格验证所有参数。

### 系统要求

| 平台 | 最低运行要求 | 发布架构 |
|---|---|---|
| Windows | Windows 10/11、Microsoft Edge WebView2 Runtime | amd64、arm64 |
| Linux | systemd、GTK 3、WebKitGTK 4.1、Ayatana AppIndicator | amd64、arm64 |
| macOS | macOS 11+ | amd64、arm64 |

CI 产物在未配置证书时是未签名包。正式分发应配置 Windows Authenticode，以及 Apple Developer ID 签名和公证。

### 安装

从版本发布页选择与系统架构一致的产物，并用 `SHA256SUMS` 校验文件。

Windows：以管理员身份运行 `cf-optimizer-<version>-windows-<arch>-setup.exe`。安装器会检查 WebView2，缺失时运行 Microsoft Evergreen Bootstrapper，然后创建配置并安装后台服务。升级安装会停止并重新启动已有服务，同时保留配置和状态。

Debian/Ubuntu：

```bash
sudo apt install ./cf-optimizer-<version>-linux-amd64.deb
```

Fedora/RHEL 系：

```bash
sudo dnf install ./cf-optimizer-<version>-linux-amd64.rpm
```

macOS：打开对应架构的 DMG 并运行其中的 PKG。未签名 CI 包可能需要在“隐私与安全性”中手动批准；正式签名发布不需要这一步。

### 首次使用

安装器会创建默认配置并启动服务。先检查状态，再进行不应用策略的测速：

```bash
cf-optimizer service-status
cf-optimizer status
cf-optimizer benchmark
```

完整优选由后台服务执行：

```bash
cf-optimizer optimize
cf-optimizer history
cf-optimizer logs --lines 100
```

诊断指定 IP 的实际出口证据：

```bash
cf-optimizer test-route 1.1.1.1
```

全局选项必须放在命令前，例如：

```bash
cf-optimizer --config /etc/cf-optimizer/config.yaml --json status
```

### 默认路径

| 平台 | 配置 | 状态、历史和日志 | IPC |
|---|---|---|---|
| Windows | `%ProgramData%\CF Optimizer\config.yaml` | `%ProgramData%\CF Optimizer` | `\\.\pipe\cf-optimizer-v1` |
| Linux | `/etc/cf-optimizer/config.yaml` | `/var/lib/cf-optimizer` | `/var/lib/cf-optimizer/daemon.sock` |
| macOS | `/Library/Application Support/CF Optimizer/config.yaml` | 同目录 | 同目录下 `daemon.sock` |

完整字段见 [config.example.yaml](config.example.yaml)。重要的安全默认值：

- `network.manage_routes: false`：默认不修改系统路由。
- `benchmark.download_url: ""`：默认不产生下载测速流量。
- 测速 Dialer 不读取 `HTTP_PROXY`、`HTTPS_PROXY` 或 `ALL_PROXY`。
- `ranges.max_change_percent` 限制远程网段异常变化。
- 代理密钥不会写入诊断导出；日志和导出还会再次脱敏。

启用路由前，应明确填写物理接口和网关，先运行 `test-route` 检查证据。不要尝试绕过 VPN Kill Switch；无法验证物理出口时应保持路由管理关闭。

### 服务管理与卸载

```bash
cf-optimizer start
cf-optimizer service-status
cf-optimizer stop
cf-optimizer cleanup
```

`cleanup` 逆序回滚持久化的受管路由和代理策略，不删除配置、日志或历史。安装器和包管理器卸载会先停止服务并执行同一清理流程；若外部程序修改了受管文件，清理会拒绝覆盖并中止卸载，便于人工核对。

Windows 使用“已安装的应用”卸载。Linux 使用 `sudo apt remove cf-optimizer` 或 `sudo dnf remove cf-optimizer`。macOS 使用：

```bash
sudo /usr/local/share/cf-optimizer/uninstall.sh
```

卸载默认保留用户配置和运行数据，重新安装可以继续使用。需要彻底删除时，应在确认不再需要历史和日志后手动移除上表中的产品专属数据目录。

### 从源码开发

需要 Go 1.23+、Node.js 22+。桌面构建还需要 Wails CLI；Linux 需要 `libgtk-3-dev`、`libwebkit2gtk-4.1-dev` 和 `libayatana-appindicator3-dev`。

```bash
go mod download
go fmt ./...
go vet ./...
go test ./...
go test -race ./...

cd desktop/frontend
npm ci
npm run lint
npm run typecheck
npm run test
npm run build
npm run e2e
```

构建当前平台桌面程序：

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.10.2
cd cmd/desktop
wails build -clean -trimpath
```

Linux 使用 WebKitGTK 4.1 时增加 `-tags webkit2_41`。打包资源见 [packaging/README.md](packaging/README.md)，设计实现和 Stitch 提示词见 [docs/DESIGN.md](docs/DESIGN.md) 与 [docs/ui/STITCH_UI_PROMPT.md](docs/ui/STITCH_UI_PROMPT.md)。

### CI 与发布

`.github/workflows/ci.yml` 包含：

- `quality`：gofmt、go vet、Go test/race、ESLint、TypeScript 和 Vitest。
- `build-core`：Windows/Linux/macOS × amd64/arm64 的 CLI 与后台服务。
- `build-desktop`：六个目标在对应原生 runner 上构建 Wails 应用。
- `e2e`：Linux 普通用户环境中的三窗口 Playwright 测试，不修改路由、Hosts、代理或系统服务。

推送 `v<major>.<minor>.<patch>` 标签会运行发布工作流，生成 Windows 安装器、Linux DEB/RPM、macOS PKG/DMG 和经验证的 `SHA256SUMS`。签名、公证需要在发布环境中单独配置证书和 Secrets。

### 当前验证边界

自动测试使用 mock 后端，不修改开发机或 CI 主机网络。跨平台编译和安装资源已覆盖，但 Windows/macOS 签名包、三平台真实服务安装、真实路由切换以及 VPN/代理组合仍需要对应真机验收。没有真实连接证据时，不应把“策略已写入”等同于“流量已直连”。

---

## English

CF Optimizer is a Windows, Linux, and macOS tool for benchmarking Cloudflare endpoints, making stable selections, and managing controlled direct-route policy. It combines an independent Go benchmark engine, a privileged background service, a diagnostic CLI, and an unprivileged Wails desktop UI built with React and Mantine.

The project does not treat a successful configuration write as proof that traffic is direct. Route and policy changes must be verified against the effective interface, gateway, route resolution, and connection evidence. When a VPN Kill Switch, operating system policy, or proxy core prevents physical egress, the tool reports that limitation and retains rollback state.

### Features

- Refresh, strictly validate, cache, and safely fall back between Cloudflare IPv4/IPv6 range snapshots.
- Generate deterministic candidates and run TCP screening, TLS verification, and optional bounded HTTPS download tests.
- Score success rate, latency, jitter, loss, and throughput, with history smoothing, cooldown, hysteresis, and minimum hold time.
- Plan, apply, verify, audit, and roll back temporary range routes and final `/32` or `/128` host routes when route management is enabled.
- Integrate with Generic Route, Mihomo, sing-box, Xray, versioned External JSON-RPC, and optional Windows Hosts adapters.
- Run under Windows Service, systemd, or LaunchDaemon. Closing the desktop window hides it to the system tray; reopening or quitting the UI does not stop the service or an active task.
- Provide eight operational views: overview, benchmark, proxy adapters, routes, ranges, history, logs/diagnostics, and settings.

### Process and privilege boundary

```text
cf-optimizer-ui  unprivileged desktop process
       |
       v
Wails Bridge -> allowlisted IPC -> privileged cf-optimizerd
                                      ^
                                      |
                                cf-optimizer CLI
```

Windows uses an ACL-protected Named Pipe; Linux and macOS use a permission-protected Unix Domain Socket. The UI cannot execute shell commands or directly edit routes, Hosts, or proxy configuration. The service validates every IPC parameter again.

### Requirements

| Platform | Minimum runtime | Architectures |
|---|---|---|
| Windows | Windows 10/11 and Microsoft Edge WebView2 Runtime | amd64, arm64 |
| Linux | systemd, GTK 3, WebKitGTK 4.1, and Ayatana AppIndicator | amd64, arm64 |
| macOS | macOS 11+ | amd64, arm64 |

CI packages are unsigned unless signing credentials are configured. Production distribution should use Windows Authenticode plus Apple Developer ID signing and notarization.

### Installation

Select the release asset matching the target architecture and verify it with `SHA256SUMS`.

Windows: run `cf-optimizer-<version>-windows-<arch>-setup.exe` as Administrator. The installer checks WebView2 and runs Microsoft's Evergreen Bootstrapper when needed, initializes configuration, and installs the background service. Upgrades stop and restart the existing service while preserving configuration and state.

Debian/Ubuntu:

```bash
sudo apt install ./cf-optimizer-<version>-linux-amd64.deb
```

Fedora/RHEL family:

```bash
sudo dnf install ./cf-optimizer-<version>-linux-amd64.rpm
```

macOS: open the architecture-specific DMG and run the enclosed PKG. Unsigned CI packages may require manual approval under Privacy & Security; properly signed production packages do not.

### First run

Installers create a default configuration and start the service. Check status, then run a benchmark that does not apply policy:

```bash
cf-optimizer service-status
cf-optimizer status
cf-optimizer benchmark
```

Ask the background service to benchmark and apply configured policy:

```bash
cf-optimizer optimize
cf-optimizer history
cf-optimizer logs --lines 100
```

Collect effective egress evidence for a target IP:

```bash
cf-optimizer test-route 1.1.1.1
```

Global options must precede the command:

```bash
cf-optimizer --config /etc/cf-optimizer/config.yaml --json status
```

### Default paths

| Platform | Configuration | State, history, and logs | IPC |
|---|---|---|---|
| Windows | `%ProgramData%\CF Optimizer\config.yaml` | `%ProgramData%\CF Optimizer` | `\\.\pipe\cf-optimizer-v1` |
| Linux | `/etc/cf-optimizer/config.yaml` | `/var/lib/cf-optimizer` | `/var/lib/cf-optimizer/daemon.sock` |
| macOS | `/Library/Application Support/CF Optimizer/config.yaml` | same directory | `daemon.sock` in the same directory |

See [config.example.yaml](config.example.yaml) for every field. Important secure defaults are:

- `network.manage_routes: false`: the default configuration does not change system routes.
- `benchmark.download_url: ""`: download benchmark traffic is disabled by default.
- Benchmark dialers do not read `HTTP_PROXY`, `HTTPS_PROXY`, or `ALL_PROXY`.
- `ranges.max_change_percent` limits abnormal remote range changes.
- Proxy secrets are excluded from diagnostics, and exported logs are redacted again.

Before enabling route management, specify the physical interface and gateway and inspect `test-route` evidence. Do not try to bypass a VPN Kill Switch; keep route management disabled when physical egress cannot be verified.

### Service management and removal

```bash
cf-optimizer start
cf-optimizer service-status
cf-optimizer stop
cf-optimizer cleanup
```

`cleanup` rolls back the persisted managed route and proxy receipt chain without deleting configuration, logs, or history. Platform uninstallers stop the service and run the same cleanup first. If another process has changed a managed file, cleanup refuses to overwrite it and aborts removal for manual review.

Use Installed Apps on Windows. On Linux, run `sudo apt remove cf-optimizer` or `sudo dnf remove cf-optimizer`. On macOS, run:

```bash
sudo /usr/local/share/cf-optimizer/uninstall.sh
```

Removal preserves user configuration and runtime state by default, so a later installation can reuse them. For complete deletion, manually remove the product-specific data directory listed above only after confirming its history and logs are no longer needed.

### Development from source

Development requires Go 1.23+ and Node.js 22+. Desktop builds also require the Wails CLI; Linux requires `libgtk-3-dev`, `libwebkit2gtk-4.1-dev`, and `libayatana-appindicator3-dev`.

```bash
go mod download
go fmt ./...
go vet ./...
go test ./...
go test -race ./...

cd desktop/frontend
npm ci
npm run lint
npm run typecheck
npm run test
npm run build
npm run e2e
```

Build the desktop application for the current platform:

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.10.2
cd cmd/desktop
wails build -clean -trimpath
```

Add `-tags webkit2_41` on Linux when using WebKitGTK 4.1. See [packaging/README.md](packaging/README.md) for packaging inputs and [docs/DESIGN.md](docs/DESIGN.md) plus [docs/ui/STITCH_UI_PROMPT.md](docs/ui/STITCH_UI_PROMPT.md) for the implemented design and Stitch prompt.

### CI and release

`.github/workflows/ci.yml` provides:

- `quality`: gofmt, go vet, Go test/race, ESLint, TypeScript, and Vitest.
- `build-core`: CLI and daemon builds for Windows/Linux/macOS on amd64/arm64.
- `build-desktop`: native Wails builds for all six targets.
- `e2e`: three-window Playwright tests with an unprivileged simulated backend that never edits routes, Hosts, proxy configuration, or services.

Pushing a `v<major>.<minor>.<patch>` tag runs the release workflow. It produces Windows installers, Linux DEB/RPM packages, macOS PKG/DMG packages, and a verified `SHA256SUMS`. Signing and notarization require certificates and Secrets configured separately in the release environment.

### Current verification boundary

Automated tests use mock backends and do not modify the developer or CI host network. Cross-platform compilation and packaging resources are covered, but signed Windows/macOS packages, real service installation on all three platforms, real route switching, and VPN/proxy combinations still require matching physical machines. Without real connection evidence, “policy written” must not be interpreted as “traffic is direct.”
