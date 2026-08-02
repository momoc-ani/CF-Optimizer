import type { RunSummary, ScheduleStatus } from '../api/types';

const scheduleTriggerLabels: Record<string, string> = {
  startup: '服务启动后执行',
  interval: '周期执行',
  network_change: '网络变化后执行',
  retry: '失败后重试',
  running: '正在执行',
};

export interface SchedulePresentation {
  value: string;
  detail: string;
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
