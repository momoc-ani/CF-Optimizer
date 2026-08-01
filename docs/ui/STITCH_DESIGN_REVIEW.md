# Stitch UI 设计评审与实现映射

评审日期：2026-08-01

Stitch 项目：[CF Optimizer Desktop Operations Admin](https://stitch.withgoogle.com/projects/4530156739579505362)

项目 ID：`4530156739579505362`

Design System：`CF Optimizer Design System`（`assets/0f41746fcf384e1689a7de4c6ed7e6ca`）

## 画板完成度

核心信息架构包含 8 个页面：总览、测速优选、代理适配、网络路由、网段管理、历史记录、日志诊断和设置。

| 设计矩阵 | Light | Dark | 合计 |
|---|---:|---:|---:|
| 1440px | 8/8 | 8/8 | 16 |
| 1024px | 8/8 | 8/8 | 16 |
| 760px | 8/8 | 8/8 | 16 |
| 核心画板 | 24/24 | 24/24 | 48/48 |

项目另有 9 个状态和补充画板：任务详情、导出确认、导出成功通知、历史记录 Mobile、日志诊断 Mobile、网段管理 Mobile、网段管理详细版，以及代理适配和设置的两个 `v2` 版本。它们作为交互状态参考，不计入 48 个核心响应式画板。

## Design System 映射

| Stitch 令牌/规则 | Mantine 与 CSS 实现 |
|---|---|
| Primary `#1677a6` | `ocean.7`，用于主命令、选中状态和活动进度 |
| Light background `#f6faff` | 页面底层背景，内容区使用白色或浅色 tonal surface |
| Dark background `#15191d` | 深色页面底层背景，内容区使用 `#1d2227` |
| 4px spacing base | 8/12/16/20/24px 的紧凑 Mantine 间距序列 |
| 6px control radius | Mantine `defaultRadius: 6`，状态 Badge 保留全圆角 |
| Inter/system font | Inter、系统字体、中文系统字体回退 |
| Data mono | IP、CIDR、事务 ID 和数值使用等宽字体与 tabular numerals |
| 64px navigation rail | 桌面 72px、窄窗口 56px 的固定图标导航轨 |
| Persistent task strip | 工作区底部固定进度条，任务与 UI 生命周期解耦 |

## 评审结果

- 导航：八个一级页面保持单层图标导航，760px 下仍可一键切换，不引入抽屉式二级导航。
- 数据密度：桌面表格维持约 32–38px 行高；窄窗口允许水平滚动，避免删除关键列或压缩长 IP。
- 危险操作：路由管理、策略应用和配置保存显示影响边界；只有后台返回验证证据后才显示“已验证”。
- 长文本：IP、CIDR、路径、错误和日志允许换行或横向滚动，不以固定宽度截断唯一诊断信息。
- 状态覆盖：页面提供加载、空数据、运行中、取消、失败、部分成功、需要权限和验证完成状态。
- WebView 可实现性：交互使用 Mantine、CSS Grid/Flex、SVG 图表和标准 Blob 导出，不依赖 Chromium 专属 API。
- 响应式：1440px 使用多列工作区，1024px 收缩侧栏和检查器，760px 将工具栏与详情区纵向排列。

## 页面实现映射

| 页面 | 前端模块 | 主要后台方法 |
|---|---|---|
| 总览 | `OverviewPage` | `system.status`、`history.list`、`routes.list`、`proxy.detect`、`ranges.get` |
| 测速优选 | `BenchmarkPage` | `optimizer.run`、`optimizer.cancel`、事件流 |
| 代理适配 | `ProxyPage` | `proxy.detect` |
| 网络路由 | `RoutesPage` | `routes.list`、`diagnostics.route` |
| 网段管理 | `RangesPage` | `ranges.get`、`ranges.update` |
| 历史记录 | `HistoryPage` | `history.list` |
| 日志诊断 | `LogsPage` | `logs.tail`、`diagnostics.route` |
| 设置 | `SettingsPage` | `config.get`、`config.update` |

普通权限 UI 不读取或修改真实路由、Hosts、代理配置和系统服务。所有系统相关操作均通过 Wails Bridge 转发到版本化本地 IPC，并由后台再次校验参数。

关闭桌面窗口时界面隐藏到系统托盘；托盘可恢复窗口或退出普通权限 UI。两种操作均不停止独立后台服务，也不取消正在执行的优选任务，重新打开界面后通过 IPC 恢复任务状态。
