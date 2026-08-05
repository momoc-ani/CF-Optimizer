import type { QuickStartResult, ResultSummary, RunSummary, ScheduleStatus } from '../api/types';

const scheduleTriggerLabels: Record<string, string> = {
  startup: '服务启动后执行',
  interval: '周期执行',
  network_change: '网络变化后执行',
  retry: '失败后重试',
  running: '正在执行',
};

const ipv4DecisionLabels: Record<string, string> = {
  'initial-selection': '首次选择节点',
  'current-remains-best': '保持当前节点：仍为第一名',
  'minimum-hold-active': '未切换：最短保持期内',
  'improvement-below-threshold': '未切换：提升未达阈值',
  'current-failure-threshold': '已切换：当前节点连续失败',
  'current-not-qualified': '已切换：当前节点不再合格',
  'better-candidate': '已切换：新节点提升达标',
  'no-qualified-candidate': '未选出合格节点',
};

export interface SchedulePresentation {
  value: string;
  detail: string;
}

export interface LatestIPv4DecisionPresentation {
  best: ResultSummary;
  selectedIP: string;
  decision: string;
}

/** presentSchedule 将后台真实调度承诺转换为总览展示文本。 */
export function presentSchedule(
  schedule: ScheduleStatus | undefined,
  running: boolean,
  formatTime: (value: string) => string,
): SchedulePresentation {
  if (!schedule) return { value: '读取中', detail: '正在读取后台调度状态' };
  if (!schedule.enabled) return { value: '已关闭', detail: '周期任务未启用' };
  if (!schedule.next_scheduled_at) {
    return {
      value: running || schedule.trigger === 'running' ? '本轮执行中' : '等待调度',
      detail: `周期 ${schedule.interval}`,
    };
  }
  const trigger = scheduleTriggerLabels[schedule.trigger ?? 'interval'] ?? '周期执行';
  return { value: formatTime(schedule.next_scheduled_at), detail: `周期 ${schedule.interval} · ${trigger}` };
}

/** formatRunEventTitle 用真实候选统计生成最近事件标题。 */
export function formatRunEventTitle(summary: RunSummary): string {
  const result = summary.error ? '部分完成' : '已完成';
  return `${result} · ${summary.qualified}/${summary.candidates} 合格`;
}

/** formatRunEventDetail 优先展示错误或切换原因，并回退到实际选中地址。 */
export function formatRunEventDetail(summary: RunSummary): string {
  if (summary.error) return summary.error;
  if (summary.switch_reason) return summary.switch_reason;
  const selections = [
    summary.selected_ipv4 ? `IPv4 ${summary.selected_ipv4}` : '',
    summary.selected_ipv6 ? `IPv6 ${summary.selected_ipv6}` : '',
  ].filter(Boolean);
  return selections.length > 0 ? selections.join(' · ') : '本次没有选出节点';
}

/** presentLatestIPv4Decision 区分本轮测速第一名与经过迟滞规则选定的 IPv4 节点。 */
export function presentLatestIPv4Decision(summary?: RunSummary): LatestIPv4DecisionPresentation | undefined {
  const best = summary?.best?.find((result) => !result.ip.includes(':'));
  if (!summary || !best) return undefined;
  const reason = summary.switch_reason?.split(';', 1)[0].trim() ?? '';
  return {
    best,
    selectedIP: summary.selected_ipv4 ?? '—',
    decision: ipv4DecisionLabels[reason] ?? (reason || '未记录切换原因'),
  };
}

/** describeQuickStartResult 只根据后端连接证据描述测速 DIRECT 状态和维护结果。 */
export function describeQuickStartResult(result: QuickStartResult): string {
  if (result.persistence_warning) return result.persistence_warning;
  if (result.error) return result.error;
  const evidence = result.report.benchmark_path?.find((item) => item.direct_verified);
  let benchmarkPath = '';
  if (evidence?.proxy_observed) {
    benchmarkPath = `测速连接已由 ${evidence.adapter} 确认为 DIRECT`;
  } else if (evidence?.physical_route_used) {
    benchmarkPath = `测速 Socket 已绑定 ${evidence.interface || '物理接口'}，且 ${evidence.adapter} 未接管该连接`;
  }
  const maintenance = result.auto_maintenance_enabled
    ? '策略与实际选路已验证，自动维护已经启用。'
    : '策略与实际选路已验证，本次运行未启用自动维护。';
  return benchmarkPath ? `${benchmarkPath}；${maintenance}` : maintenance;
}
