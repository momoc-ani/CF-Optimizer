# CF Optimizer 项目上下文与关键决策

版本：1.0  
整理日期：2026-08-01

## 1. 背景

最初环境使用 Windows 版 CloudflareSpeedTest v2.3.5，目录中包含 `cfst.exe`、Cloudflare IP 列表、测速结果以及更新 Hosts/3Proxy 的旧批处理脚本。

旧方案存在以下问题：

- 自动化依赖批处理和外部可执行文件。
- Hosts/3Proxy脚本首次使用需要手工输入旧IP。
- 没有稳定处理系统代理、Clash/Mihomo TUN和其他VPN的绕行。
- 缺少跨平台支持。
- 缺少后台服务、状态管理、故障回退和可视化界面。
- 代理规则可能在订阅刷新后丢失。

当前Windows环境检测到Clash Verge/Mihomo。现有配置中已有手工维护的Cloudflare `/32 DIRECT` 规则，说明节点直连规则目前依赖人工更新。具体订阅、节点和认证信息不写入项目文档。

## 2. 已确认需求

- 不依赖现有CFST程序，自主实现测速和优选核心。
- 支持Windows、Linux和macOS桌面平台。
- 支持amd64和arm64。
- 自动更新Cloudflare官方网段。
- 自动测速并选择最优IPv4/IPv6节点。
- 测速流量不能经过系统代理、Clash/Mihomo或其他VPN代理链路。
- 最终Cloudflare节点应走真实物理网络。
- 作为系统后台服务开机自动运行。
- 支持周期测速和网络变化触发复测。
- Clash/Mihomo只是适配层之一，还需要支持其他代理内核和通用策略。
- 使用现代前端UI框架提供桌面界面。

## 3. 已确定技术方案

### 3.1 核心语言

选择Go，原因如下：

- 网络并发和Socket控制能力适合测速服务。
- 可以输出独立单文件，无需安装语言运行时。
- Windows、Linux、macOS均有成熟系统服务实现路径。
- Wails能够直接复用Go后端模型和方法。
- 相比Rust，跨平台开发周期和团队进入成本更低。

### 3.2 桌面界面

选择Wails + React + TypeScript + Mantine：

- Wails使用系统WebView，不捆绑完整Chromium。
- React生态适合实时状态、表格和配置界面。
- Mantine组件完整，适合桌面运维工具。
- TanStack Query维护服务状态和缓存。
- TanStack Table展示候选IP与历史结果。
- Zustand仅保存页面和交互状态。
- ECharts展示延迟、速度和历史趋势。

不选择Electron作为默认方案，主要原因是安装体积和内存占用较大。不选择Tauri作为默认方案，主要原因是Go核心之外还要长期维护Rust层。

### 3.3 特权边界

桌面界面不以管理员/root权限长期运行。

```text
普通权限UI
    │ Wails Bridge
    ▼
本地受限IPC
    ▼
特权后台服务
```

平台IPC：

- Windows：Named Pipe + ACL。
- Linux/macOS：Unix Domain Socket + 文件权限。

后台服务负责路由、Hosts、网卡绑定、代理策略和测速；界面只负责显示和发出经过验证的命令。

## 4. 系统路由与代理的关系

系统IP路由并不绝对高于代理软件，需要根据代理模式处理。

### 4.1 普通HTTP/SOCKS系统代理

如果应用主动使用系统代理，它实际连接的是本地代理端口，目标IP的系统路由不会被使用。因此测速程序必须明确禁用代理环境，并使用直接Socket。

### 4.2 TUN代理

当TUN只通过默认路由或两个 `/1` 路由接管流量时，目标IP的 `/32` 或 `/128` 物理路由通常具有更高前缀优先级。但如果TUN使用策略路由、驱动过滤或同等具体的路由，普通主机路由不保证有效。

### 4.3 透明代理和强制VPN

- Linux nftables/iptables TPROXY可能通过fwmark和`ip rule`绕过主路由表。
- Windows WFP驱动可以在普通路由之外拦截或重定向连接。
- macOS Network Extension Packet Tunnel可以接管系统流量。
- VPN Kill Switch可能明确禁止物理网卡直连。

因此完整绕行采用三层方案：

1. 测速Socket绑定物理网卡且不使用系统代理。
2. 候选网段临时路由和最终节点主机路由指向物理网关。
3. 代理适配器写入进程、IP或域名DIRECT/Exclude策略。

只有实际连接验证成功后，界面才显示“已验证直连”，不能只根据路由存在就宣称成功。

## 5. 测速设计

### 5.1 候选生成

- IPv4大网段逻辑划分为 `/24` 后抽样。
- IPv6根据网段和每日种子生成确定性随机地址。
- 默认候选总量限制在500–2000个。
- 历史优秀节点优先进入候选集。
- 连续失败地址进入冷却期。

### 5.2 两阶段测速

第一阶段：

- TCP 443连接测试，不依赖ICMP。
- 每个IP测试4–6次。
- 统计成功率、平均延迟、P95和抖动。
- 淘汰丢包和延迟不合格节点。

第二阶段：

- 对前10–30名执行TLS和HTTPS下载测速。
- 连接目标IP，但保留正确的SNI和HTTP Host。
- 限制单节点测试时间和下载流量。
- 统计TLS时间、首字节时间和稳定吞吐量。

### 5.3 选择与切换

综合评分包括速度、延迟、丢包、抖动、历史成功率和最近失败次数。

默认只有新节点比当前节点综合改善15%以上，或当前节点连续失败时才切换。切换过程先应用并验证新节点，再删除旧节点策略，避免中断。

## 6. Cloudflare 网段更新

主数据源：

```text
https://api.cloudflare.com/client/v4/ips
```

备用数据源：

```text
https://www.cloudflare.com/ips-v4
https://www.cloudflare.com/ips-v6
```

更新策略：

- 程序内置已验证快照。
- 默认每24小时更新并加入随机抖动。
- 保存当前版本和上一版本。
- 严格校验CIDR、地址类型、数量和变化比例。
- 异常数据不应用，继续使用本地缓存。
- 支持用户 `include` 和 `exclude`。
- 正在运行的测速使用不可变网段快照，不被中途更新影响。

如果当前最优IP从官方网段中移除，先重新测速并验证新节点，再删除旧路由。

## 7. 代理适配策略

适配目标按代理内核划分，而不是按客户端品牌划分：

- Mihomo/Clash。
- sing-box。
- Xray/V2Ray。
- Surge。
- WireGuard。
- OpenVPN。
- Generic Route。
- External Hook。

统一策略包含进程名、IPv4 CIDR、IPv6 CIDR、域名和直连模式。

策略应用优先级：

1. 官方控制API。
2. Overlay、Mixin、Rule Provider。
3. 带程序标记的配置区块。
4. 系统路由。
5. 用户自定义脚本。

每个适配器必须支持能力声明、检测、变更计划、应用、验证和回滚。不能无标记地重写完整订阅文件。

第三方扩展采用独立进程加版本化JSON-RPC，不使用Go原生插件。

## 8. 后台服务

平台方式：

- Windows：Windows Service。
- Linux：systemd。
- macOS：LaunchDaemon。

默认行为：

- 开机后网络可用时测速一次。
- 每6小时重新优选。
- 默认网关或网络接口变化后快速复测。
- 同时只运行一个测速任务。
- 失败指数退避。
- 系统服务管理器负责异常重启。

统一命令：

```text
cf-optimizer install
cf-optimizer uninstall
cf-optimizer start
cf-optimizer stop
cf-optimizer run
cf-optimizer status
cf-optimizer test-route
```

## 9. 桌面界面

首版页面：

- 总览。
- 测速优选。
- 代理适配。
- 网络和路由。
- Cloudflare网段管理。
- 历史记录。
- 日志和诊断。
- 设置。

界面定位为紧凑、安静的桌面运维工具，不设计营销落地页或装饰性大卡片。

## 10. 打包体积预估

使用系统WebView时：

| 平台 | 安装包 | 安装后 |
|---|---:|---:|
| Windows | 18–35 MB | 35–60 MB |
| macOS DMG | 20–40 MB | 40–70 MB |
| Linux TAR.GZ（内含 DEB/RPM） | 20–50 MB | 30–55 MB |

Windows如果完整捆绑固定版WebView2，可能额外增加150–250 MB，因此默认只检查系统WebView2并在缺失时安装。

构建时使用 `-trimpath -ldflags="-s -w"`，不使用UPX，以减少安全软件误报并保证代码签名稳定。

## 11. 发布目标

```text
windows-amd64
windows-arm64
linux-amd64
linux-arm64
darwin-amd64
darwin-arm64
```

每个目标只公开一个安装包：Windows 使用 EXE，Linux 使用同时包含 DEB、RPM 和统一安装脚本的 TAR.GZ，macOS 使用内含 PKG 的 DMG。发布页另提供覆盖六个安装包的 `SHA256SUMS`。

Windows正式发布建议代码签名，macOS正式发布需要Apple Developer签名和公证。

## 12. 时间估算

- 独立测速CLI MVP：3–4周。
- 三个平台后台服务和首批代理适配器：5–7周。
- 完整界面、安装、升级和稳定性测试：单开发者约8–10周。
- 两名开发者并行：约6–8周。

## 13. 已知风险

- 强制Kill Switch无法仅靠路由绕过。
- Fake-IP可能影响域名解析，需要绑定物理接口解析或直接使用目标IP。
- 订阅刷新可能覆盖代理规则，需要幂等检测和恢复。
- 临时路由残留可能影响网络，需要事务日志和启动恢复。
- 第三方测速地址可能限流，应使用项目自有Cloudflare测速域名。
- macOS签名、公证和Linux桌面依赖会影响发布流程。

## 14. 开发前待确认事项

1. 首版是否默认启用IPv6测速。
2. 项目自有Cloudflare测速域名和最大带宽成本。
3. Linux 已确认支持 Debian/Ubuntu 与 Fedora/RHEL 系；仍需确定首轮真机验收的具体发行版版本。
4. 需要优先验证的代理客户端和版本。
5. Windows和macOS代码签名证书安排。
6. 首版是否包含应用自动更新。
7. Hosts更新是否默认关闭。

## 15. 推荐首个里程碑

先实现以下闭环：

```text
Go独立测速CLI
→ Cloudflare网段更新
→ Windows/Linux物理直连验证
→ Generic Route
→ Mihomo适配
→ 后台服务最小版本
```

闭环验证通过后，再增加macOS、sing-box、Xray和完整Wails桌面界面。
