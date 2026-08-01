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
- 通过 Windows Service、systemd 或 LaunchDaemon 运行后台服务；关闭桌面窗口会隐藏到系统托盘，退出 UI 不会停止服务或正在运行的任务。
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

### 平台使用指南

从版本发布页下载对应架构的安装包和 `SHA256SUMS`，然后按目标系统指南操作。CI 产物在未配置证书时可能未签名。

| 平台 | 发布产物 | 最低运行要求 | 完整指南 |
|---|---|---|---|
| Windows amd64/arm64 | `*-windows-*-setup.exe` | Windows 10/11、WebView2 Runtime | [Windows 安装与使用](docs/platforms/windows.md#中文) |
| macOS amd64/arm64 | `*-darwin-*.dmg`，内含 PKG | macOS 11+ | [macOS 安装与使用](docs/platforms/macos.md#中文) |
| Linux amd64/arm64 | `*-linux-*.deb` 或 `*.rpm` | systemd、GTK 3、WebKitGTK 4.1、支持 StatusNotifier 的桌面环境 | [Linux 安装与使用](docs/platforms/linux.md#中文) |

在 WSL 中不要安装 Linux 后台服务来管理 Windows 主机网络，应在 Windows 主机中使用 Windows 安装包。

### 安全默认值

完整配置字段见 [config.example.yaml](config.example.yaml)。重要默认值如下：

- `network.manage_routes: false`：默认不修改系统路由。
- `benchmark.download_url: ""`：默认不产生下载测速流量。
- 测速 Dialer 不读取 `HTTP_PROXY`、`HTTPS_PROXY` 或 `ALL_PROXY`。
- `ranges.max_change_percent` 限制远程网段异常变化。
- 代理密钥不会写入诊断导出；日志和导出还会再次脱敏。

启用路由前，应明确填写物理接口和网关，先运行路由诊断并检查实际证据。不要尝试绕过 VPN Kill Switch；无法验证物理出口时应保持路由管理关闭。

### 从源码开发

需要 Go 1.23+、Node.js 22+。桌面构建还需要 Wails CLI；Linux 额外需要 `libgtk-3-dev` 和 `libwebkit2gtk-4.1-dev`。

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

自动测试使用 mock 后端，不修改开发机或 CI 主机网络。跨平台编译和安装资源已覆盖，但 Windows/macOS 签名包、三平台真实服务安装、真实托盘交互、真实路由切换以及 VPN/代理组合仍需要对应真机验收。没有真实连接证据时，不应把“策略已写入”等同于“流量已直连”。

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
- Run under Windows Service, systemd, or LaunchDaemon. Closing the desktop window hides it to the system tray; quitting the UI does not stop the service or an active task.
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

### Platform guides

Download the installer for the target architecture and `SHA256SUMS` from the release page, then follow the corresponding guide. CI artifacts may be unsigned when signing credentials are not configured.

| Platform | Release asset | Minimum runtime | Full guide |
|---|---|---|---|
| Windows amd64/arm64 | `*-windows-*-setup.exe` | Windows 10/11 and WebView2 Runtime | [Install and use on Windows](docs/platforms/windows.md#english) |
| macOS amd64/arm64 | `*-darwin-*.dmg` containing a PKG | macOS 11+ | [Install and use on macOS](docs/platforms/macos.md#english) |
| Linux amd64/arm64 | `*-linux-*.deb` or `*.rpm` | systemd, GTK 3, WebKitGTK 4.1, and a StatusNotifier-capable desktop | [Install and use on Linux](docs/platforms/linux.md#english) |

Do not install the Linux service under WSL to manage Windows host networking. Install the Windows package on the Windows host instead.

### Secure defaults

See [config.example.yaml](config.example.yaml) for every field. Important defaults are:

- `network.manage_routes: false`: the default configuration does not change system routes.
- `benchmark.download_url: ""`: download benchmark traffic is disabled by default.
- Benchmark dialers do not read `HTTP_PROXY`, `HTTPS_PROXY`, or `ALL_PROXY`.
- `ranges.max_change_percent` limits abnormal remote range changes.
- Proxy secrets are excluded from diagnostics, and exported logs are redacted again.

Before enabling route management, specify the physical interface and gateway, run route diagnostics, and inspect the resulting evidence. Do not try to bypass a VPN Kill Switch; keep route management disabled when physical egress cannot be verified.

### Development from source

Development requires Go 1.23+ and Node.js 22+. Desktop builds also require the Wails CLI. Linux additionally requires `libgtk-3-dev` and `libwebkit2gtk-4.1-dev`.

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

Automated tests use mock backends and do not modify the developer or CI host network. Cross-platform compilation and packaging resources are covered, but signed Windows/macOS packages, real service installation on all three platforms, real tray interaction, real route switching, and VPN/proxy combinations still require matching physical machines. Without real connection evidence, “policy written” must not be interpreted as “traffic is direct.”
