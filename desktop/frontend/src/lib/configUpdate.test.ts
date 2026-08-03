import { describe, expect, it } from 'vitest';
import { describeConfigUpdateResult } from './configUpdate';

describe('describeConfigUpdateResult', () => {
  it('reports an immediately applied domain update', () => {
    expect(describeConfigUpdateResult({ saved: true, hot_applied: true, restart_required: false })).toBe('更改已经应用。');
  });

  it('separates hot-applied domains from pending settings', () => {
    expect(describeConfigUpdateResult({ saved: true, hot_applied: true, restart_required: true })).toContain('域名列表已即时应用');
  });

  it('reports settings that still require a daemon restart', () => {
    expect(describeConfigUpdateResult({ saved: true, hot_applied: false, restart_required: true })).toContain('后台服务重启后应用');
  });
});
