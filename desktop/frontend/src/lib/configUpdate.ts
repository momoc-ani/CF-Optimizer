export interface ConfigUpdateResult {
  saved: boolean;
  hot_applied: boolean;
  policy_refreshed: boolean;
  restart_required: boolean;
}

/** describeConfigUpdateResult 区分即时生效的域名列表与仍需重启的其他配置。 */
export function describeConfigUpdateResult(result: ConfigUpdateResult): string {
  if (result.hot_applied && !result.policy_refreshed) {
    if (result.restart_required) return '域名列表已保存但当前没有活动策略；其余更改将在后台服务重启后应用。';
    return '域名列表已保存；当前没有活动策略，尚未应用。';
  }
  if (result.restart_required && result.hot_applied) return '域名列表已即时应用；其余更改将在后台服务重启后应用。';
  if (result.restart_required) return '已保存；后台服务重启后应用更改。';
  return '更改已经应用。';
}
