import { describe, expect, it } from 'vitest';
import { describeConfigUpdateResult } from './configUpdate';

describe('describeConfigUpdateResult', () => {
  it('reports an immediately applied domain update', () => {
    expect(describeConfigUpdateResult({ saved: true, hot_applied: true, policy_refreshed: true, restart_required: false })).toContain('策略也已重新验证');
  });

  it('separates hot-reloaded settings from process-bound settings', () => {
    expect(describeConfigUpdateResult({ saved: true, hot_applied: true, policy_refreshed: true, restart_required: true })).toContain('IPC 端点或数据目录');
  });

  it('reports settings that still require a daemon restart', () => {
    expect(describeConfigUpdateResult({ saved: true, hot_applied: false, policy_refreshed: false, restart_required: true })).toContain('后台服务重启后应用');
  });

  it('reports a hot reload that does not require policy refresh', () => {
    expect(describeConfigUpdateResult({ saved: true, hot_applied: true, policy_refreshed: false, restart_required: false })).toContain('当前无需刷新策略');
  });
});
