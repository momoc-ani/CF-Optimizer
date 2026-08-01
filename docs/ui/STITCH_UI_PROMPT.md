# CF Optimizer Stitch UI 设计提示词

版本：1.0  
日期：2026-08-01  
目标：生成可由 Wails + React + Mantine 落地的跨平台桌面运维界面设计稿。

## 使用方式

将“Stitch 主提示词”完整粘贴到 Stitch。要求 Stitch 为每个核心页面同时输出宽屏、紧凑窗口、窄窗口设计，并分别给出浅色与深色主题。后续迭代时保留“不可变设计约束”，仅补充需要修改的页面或状态。

## Stitch 主提示词

```text
Design a production-ready cross-platform desktop operations application named "CF Optimizer". It manages a privileged background service that benchmarks Cloudflare IP addresses, selects stable nodes, and applies verified direct-route and proxy policies. This is the actual application workspace, not a landing page, onboarding page, or marketing site.

Audience and tone
- Audience: technical users and network operators on Windows, Linux, and macOS.
- Tone: quiet, precise, trustworthy, compact, and work-focused.
- Prioritize scanning, comparison, repeated operations, evidence, and recovery.
- Use concise Simplified Chinese UI copy in the mockups. Keep protocol names, IP addresses, adapter names, and technical identifiers in English.
- Never claim that a route or DIRECT policy is effective unless a verification result is visible.

Required design variants
- Desktop wide: 1440 x 900.
- Compact desktop window: 1024 x 720.
- Narrow desktop window: 760 x 800. This is a resized desktop window, not a mobile app.
- Produce a coherent light theme and dark theme.
- Show at least one normal state and the specified exceptional states for every workflow.

Global shell and navigation
- Use a persistent left navigation rail on wide and compact layouts. Width approximately 216 px wide, 72 px collapsed. On narrow layout use a compact icon rail or drawer without turning the app into mobile navigation.
- Top area contains the current page title, daemon connection state, privilege state, theme control, and a compact overflow menu.
- Navigation items, in order: 总览, 测速优选, 代理适配, 网络路由, 网段管理, 历史记录, 日志诊断, 设置.
- Use Lucide icons for navigation and familiar actions. Every icon-only action needs a tooltip and an accessible label.
- Keep the current run visible across pages through a slim persistent task strip with stage, processed/total, elapsed time, and cancel action. Do not show it when no task exists.
- The Wails UI is always unprivileged. Any system-changing action must visibly go through the daemon and show permission, apply, verify, and rollback states.

Visual system
- Use Mantine-compatible components and spacing. Cards may represent individual metrics or repeated records, but do not place cards inside cards and do not turn full page sections into floating cards.
- Border radius: 6 px for fields, buttons, tables, panels, and dialogs; no pill-shaped general controls. Status badges may be pill-shaped.
- Use a neutral gray base with restrained semantic accents: verified green, information cyan/blue, warning amber, destructive red, and violet only for chart series differentiation. Do not use gradients, decorative blobs, glass effects, oversized shadows, or ornamental illustration.
- Light theme background #F6F7F8, surface #FFFFFF, primary text #1D252C, muted text #65717B, border #DDE2E6.
- Dark theme background #15191D, surface #1D2227, primary text #EEF1F3, muted text #9BA6AE, border #353C42.
- Suggested semantic colors: primary/action #1677A6, verified #2B8A5A, warning #C47A12, danger #C33D4A, inactive #75808A.
- Use a 4 px spacing base with common gaps of 8, 12, 16, and 24 px. Page padding 20-24 px wide and 16 px narrow.
- Use a system UI font stack. Body 13-14 px, compact labels 12 px, page titles 20-22 px, section headings 15-16 px. Letter spacing must be 0. Do not scale font with viewport width.
- Tables use stable column widths, sticky headers where useful, row height 40-44 px, tabular numerals for latency, speed, scores, addresses, and timestamps.
- Clearly visible keyboard focus, sufficient contrast, no color-only status communication, and no text overlap or truncation without tooltip/full-value access.

Shared status language
- Daemon: 已连接, 正在连接, 版本不兼容, 服务未运行.
- Permission: 普通权限, 需要管理员权限, 权限已授予.
- Verification: 未验证, 验证中, 已验证, 验证失败, 已回滚.
- Run: 准备中, 更新网段, 生成候选, TCP 初筛, TLS/下载复筛, 选择节点, 应用策略, 验证, 完成, 已取消, 失败, 部分成功.
- Every status must combine an icon, text, and semantic color.

Screen 1: 总览
- First viewport must immediately show service health and current selection, not a hero.
- Top status band: daemon connection, scheduler enabled/disabled, next run time, last successful run, and a compact "立即优选" primary action with Play icon.
- Current IPv4 and IPv6 rows: selected IP, score, latency, loss, throughput, selected time, physical interface, gateway, policy status, and verification evidence.
- Operational summary: detected proxy adapters and their verified/direct states; route transaction summary; Cloudflare range freshness.
- Small 24-hour trend chart for score/latency with an explicit no-data state.
- Recent events list with severity, timestamp, concise message, and link to diagnostics.
- Show variants for disconnected daemon, no selection yet, partial success where IPv4 succeeds and IPv6 fails, and a fully verified normal state.

Screen 2: 测速优选
- Dense run toolbar: start, cancel, force range refresh checkbox, apply policy toggle, family segmented control (IPv4 / IPv6 / 双栈), and latest run selector.
- During a run, show stage timeline, processed count, success count, elapsed time, estimated remaining time if known, and aggregate errors. Do not let progress content resize the surrounding layout.
- Main candidate table columns: rank, IP, family, TCP success, average latency, P95, jitter, TLS, TTFB, throughput, score, decision/status. Support sort, filter, family filter, column visibility, and row details.
- Charts: latency distribution and top-node throughput/score comparison. Charts must have legends, units, accessible colors, tooltips, and empty states.
- Row details panel shows raw evidence and warnings without exposing sensitive data.
- Show normal idle, loading, active, cancelling, cancelled, failed, partial success, and complete states.

Screen 3: 代理适配
- Show a compact adapter table/list for Generic Route, Mihomo, sing-box, Xray, External JSON-RPC, and optional Windows Hosts.
- Columns: detected, enabled, capability, apply mechanism, last applied, verification, and action menu.
- Selected adapter detail uses tabs: 概览, 配置, 变更计划, 验证记录.
- A policy preview displays processes, IPv4 /32 entries, IPv6 /128 entries, and domains.
- Applying a policy opens a confirmation dialog that states the exact adapter and impact. Then show plan -> apply -> verify -> rollback lifecycle, transaction ID, and final evidence.
- Never display secrets, tokens, full third-party configuration files, or authorization headers.
- Show not detected, needs configuration, validation failure, reload failure with successful rollback, partial success, and verified states.

Screen 4: 网络路由
- Physical path summary: interface name/index, type, source addresses, IPv4 gateway, IPv6 gateway, and confidence/warnings.
- Route table separates temporary benchmark routes and persistent selected-host routes. Columns: target prefix, gateway, interface, metric, transaction, phase, verification, created time.
- Diagnostic inspector for a target IP shows expected route, observed route, source address, proxy/TUN interfaces detected, and limitations such as Kill Switch.
- Route management has a clear disabled-by-default control and a permission warning. Enabling it requires explicit confirmation; never imply success before verification.
- Provide transaction timeline and rollback action for failed or interrupted operations.

Screen 5: 网段管理
- Source and freshness header: API source, ETag/hash, fetched time, age, next refresh, status, and refresh action.
- IPv4 and IPv6 range tables with CIDR, source, inclusion state, and validation result.
- Difference viewer for current versus previous snapshot: added, removed, unchanged, percentage change, and rejection reason.
- Include/exclude editor uses validated CIDR rows, not a free-form unstructured text area. Provide add, remove, inline error, and conflict indication.
- Show loading, offline cache fallback, 304 unchanged, stale cache, rejected abnormal update, and successful refresh states.

Screen 6: 历史记录
- Run history table columns: start time, duration, range hash/source, candidates, selected IPv4, selected IPv6, policy result, warnings, final result.
- Filters for time range, address family, result, and switch/no-switch.
- Detail drawer/page shows score changes, selection reason, hysteresis/minimum-hold decision, top candidates, and related transaction IDs.
- Trend chart compares selected score, latency, loss, and throughput over time without mixing incompatible units on one unlabeled axis.
- Empty history and retention-expired states must be designed.

Screen 7: 日志诊断
- Split workspace with filter toolbar and virtualized-looking log table. Filters: level, component, run ID, transaction ID, time, and search.
- Log columns: time, level, component, event/message. Expand row for structured fields.
- Sensitive values appear redacted. Long values wrap or open in a details view; they never overlap adjacent content.
- Diagnostics area runs route diagnostics against a validated IP and displays evidence sections, limitations, and remediation hints.
- Export report action uses Download icon and explicitly states that secrets are filtered.
- Show log loading, no matches, stream disconnected/reconnecting, export success, and export failure.

Screen 8: 设置
- Use unframed sections or tabs: 调度, 测速, 网络, 代理, Hosts, 存储与日志, 关于.
- Build forms with labels, descriptions only where a safety boundary needs explanation, inline validation, units, and current/default values.
- Use switches for booleans, segmented controls for small modes, numeric inputs/sliders for bounded numeric values, select menus for option sets, and text inputs for URLs/paths.
- High-risk settings (route management, Hosts changes, external executable) have warning callouts and require explicit save confirmation.
- Sticky action footer: 未保存更改 indicator, reset, validate, save. Save states: validating, saving, saved, failed, restart required.
- Include IPC version and application/service version mismatch presentation in 关于.

Dialogs and overlays
- Confirmation dialog: concise impact summary, affected adapters/routes, permission status, cancel and explicit action.
- Run details and record details should use a side drawer where width permits and a full content overlay in narrow windows.
- Toast notifications are only for short acknowledgements; persistent failures and recovery actions remain in page context.
- Destructive actions use red only where destructive intent is real. Use familiar Lucide icons instead of text-only rounded controls when an icon is sufficient.

Responsive behavior
- At 1440 px, use two-column analytical layouts where comparison benefits.
- At 1024 px, collapse secondary detail panels into drawers and let tables scroll horizontally with pinned identity/status columns.
- At 760 px, collapse navigation, stack summary bands, keep commands reachable, preserve table minimum widths with controlled horizontal scrolling, and prevent any button label or IP address from overlapping.
- Fixed-format metrics, chart areas, toolbars, and progress controls need stable min/max dimensions to prevent layout shift.

Required component/state sheet
- Include a separate component sheet for buttons, icon buttons/tooltips, inputs, toggles, segmented controls, tabs, badges, alerts, progress, skeletons, tables, pagination, empty states, dialogs, drawers, charts, task strip, transaction timeline, and verification evidence.
- For each interactive component show default, hover, focus, disabled, loading, success, warning, and error where applicable.
- Include a semantic status matrix mapping every run, permission, daemon, route, proxy, and verification state to icon, label, and color.

Output expectations
- Produce named frames for every page and viewport/theme variant.
- Use realistic Chinese labels, IPv4/IPv6 examples, durations, timestamps, and metrics, while marking data as sample data.
- Annotate interaction transitions for start/cancel, apply/verify/rollback, settings validation/save, daemon reconnect, and version mismatch.
- Keep every pattern implementable using Mantine, TanStack Query/Table, Zustand, React Hook Form/Zod, ECharts, and Lucide in a Wails WebView.
```

## 不可变设计约束

- UI 只能通过 Wails Bridge 连接受限 IPC，不展示或设计直接执行 shell、编辑系统文件的入口。
- 路由、Hosts、代理策略必须呈现“计划 -> 应用 -> 验证 -> 回滚”的状态和事务证据。
- 未获得验证证据时，文案只能使用“未验证”“应用待验证”或“验证失败”，不能显示“直连已生效”。
- 运行中任务关闭窗口后仍由后台服务继续；重新打开 UI 时必须能恢复任务进度。
- 服务端数据归 TanStack Query，页面筛选与临时交互状态归 Zustand，设计稿不得依赖两份互相覆盖的数据源。
- 危险设置默认关闭；密钥、认证头、完整第三方配置及敏感日志均不得出现在设计稿中。

## 设计稿评审清单

### 信息架构

- 八个页面在不超过两次操作内可达，当前页面和后台任务状态始终明确。
- 总览首屏能回答服务是否可用、当前节点是什么、策略是否经过验证、何时再次测速。
- 高频操作不依赖多层菜单，低频详情不会挤压主工作区。

### 状态完整性

- 每个长任务均有加载、空数据、运行中、取消中、已取消、失败、部分成功和完成状态。
- 服务断开、协议版本不兼容、需要权限、权限拒绝和恢复连接均有稳定页面状态。
- 应用成功与验证成功是两个独立状态；验证失败后能看到回滚结果和事务 ID。

### 数据与交互

- IP、CIDR、路径、错误消息和事务 ID 的长文本不会覆盖相邻内容，可复制并可查看完整值。
- 表格支持键盘焦点、排序、筛选、空结果和横向滚动；紧凑窗口仍保留身份列和状态列。
- 图表标明单位、图例和数据范围，不依靠颜色作为唯一信息通道。

### 视觉与可实现性

- 不使用营销式 Hero、渐变、装饰光斑、玻璃效果、嵌套卡片或大面积单一色调。
- 控件可直接映射到 Mantine；图标来自 Lucide；图表可由 ECharts 实现。
- 1440x900、1024x720、760x800 均无重叠、溢出或关键操作不可达问题。
- 浅色和深色主题均满足文字、边框、焦点和语义状态对比度。

### 交付记录

Stitch 输出后应记录设计稿链接、生成日期、评审结论和偏差项。当前代码实现以本文件的不可变约束和设计令牌为基线；设计稿若改变权限边界、架构或技术栈，必须先更新实施计划并重新审查。
