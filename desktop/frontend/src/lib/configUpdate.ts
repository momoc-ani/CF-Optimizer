export interface ConfigUpdateResult {
  saved: boolean;
  hot_applied: boolean;
  policy_refreshed: boolean;
  restart_required: boolean;
}

/** describeConfigUpdateResult 区分热重载、策略刷新和少量进程边界配置。 */
export function describeConfigUpdateResult(result: ConfigUpdateResult): string {
  if (result.restart_required) return result.hot_applied ? '可热重载的更改已应用；IPC 端点或数据目录将在后台服务重启后应用。' : '已保存；IPC 端点或数据目录将在后台服务重启后应用。';
  if (result.hot_applied && result.policy_refreshed) return '配置已热重载，当前策略也已重新验证。';
  if (result.hot_applied) return '配置已热重载，当前无需刷新策略。';
  return '配置没有运行时变化。';
}
