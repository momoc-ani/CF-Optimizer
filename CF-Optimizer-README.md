# CF Optimizer

CF Optimizer 是一个面向 Windows、Linux 和 macOS 的 Cloudflare 节点自动测速、优选和直连管理工具。

使用 Go 实现独立测速核心，并通过 Wails + React + Mantine 提供跨平台桌面界面。

## 核心能力

- 自动更新并校验 Cloudflare 官方 IPv4、IPv6 网段。
- 自动生成候选 IP，测试连接成功率、延迟、抖动、TLS 和下载速度。
- 测速 Socket 绑定物理网卡，测速期间建立临时直连路由。
- 自动选择稳定的最优节点，使用迟滞策略避免频繁切换。
- 为最终节点维护 `/32` 或 `/128` 物理网关路由。
- 通过适配层为 Mihomo/Clash、sing-box、Xray 等代理内核应用 DIRECT 策略。
- Windows Service、systemd 和 LaunchDaemon 后台自动运行。
- 提供状态、测速、代理适配、路由、网段、历史和日志界面。

## 技术栈

- 后台服务、CLI、测速核心：Go
- 桌面容器：Wails
- 前端：React + TypeScript + Vite
- UI：Mantine
- 数据请求与表格：TanStack Query、TanStack Table
- 本地界面状态：Zustand
- 图表：Apache ECharts
- 表单：React Hook Form + Zod

## 进程模型

```text
cf-optimizerd      特权后台服务
cf-optimizer-ui    普通权限桌面界面
cf-optimizer       服务管理和诊断CLI
```

桌面界面通过 Named Pipe 或 Unix Domain Socket 与后台服务通信。关闭界面不会停止后台服务。

## 文档

- `docs/PROJECT_CONTEXT.md`：需求、技术决策、架构边界和待确认事项。
- `docs/IMPLEMENTATION_PLAN.md`：模块划分、开发阶段、测试和验收计划。

## 当前状态

当前处于设计和实施计划阶段，尚未开始正式编码。

推荐首个里程碑：独立 Go 测速 CLI + Generic Route + Mihomo 适配 + Windows/Linux 验证。
