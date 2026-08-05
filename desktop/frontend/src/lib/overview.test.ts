import { describe, expect, it } from 'vitest';
import type { QuickStartResult, RunSummary } from '../api/types';
import { describeQuickStartResult, formatRunEventDetail, formatRunEventTitle, presentLatestIPv4Decision, presentSchedule } from './overview';

describe('overview presentation', () => {
  it('shows the exact next execution time promised by the daemon', () => {
    const result = presentSchedule(
      { enabled: true, interval: '6h0m0s', next_scheduled_at: '2026-08-02T09:30:00Z', trigger: 'interval' },
      false,
      (value) => `formatted:${value}`,
    );
    expect(result).toEqual({
      value: 'formatted:2026-08-02T09:30:00Z',
      detail: '周期 6h0m0s · 周期执行',
    });
  });

  it('does not invent a next execution time while disabled or running', () => {
    expect(presentSchedule({ enabled: false, interval: '6h0m0s', trigger: 'disabled' }, false, String).value).toBe('已关闭');
    expect(presentSchedule({ enabled: true, interval: '6h0m0s', trigger: 'running' }, true, String).value).toBe('本轮执行中');
  });

  it('uses real run statistics and failure details for recent events', () => {
    const summary: RunSummary = {
      id: 'run-1', started_at: '2026-08-02T09:00:00Z', finished_at: '2026-08-02T09:01:00Z',
      candidates: 1000, qualified: 27, error: 'IPv6 未选出合格节点',
    };
    expect(formatRunEventTitle(summary)).toBe('部分完成 · 27/1000 合格');
    expect(formatRunEventDetail(summary)).toBe('IPv6 未选出合格节点');
  });

  it('区分最近测速第一名与本轮实际选定节点', () => {
    const summary: RunSummary = {
      id: 'run-2', started_at: '2026-08-05T03:25:33Z', finished_at: '2026-08-05T03:26:20Z',
      candidates: 1000, qualified: 20, selected_ipv4: '104.18.44.43',
      switch_reason: 'improvement-below-threshold; no-qualified-candidate',
      best: [
        { ip: '162.159.61.137', score: 94.8, avg_latency: 46_073_179, loss: 0, mbps: 0 },
        { ip: '104.18.44.43', score: 94.47, avg_latency: 48_061_130, loss: 0, mbps: 0 },
      ],
    };

    expect(presentLatestIPv4Decision(summary)).toEqual({
      best: summary.best![0],
      selectedIP: '104.18.44.43',
      decision: '未切换：提升未达阈值',
    });
  });

  it('describes DIRECT only when benchmark connection evidence is verified', () => {
    const result = {
      status: 'verified', mode: 'apply_once', auto_maintenance_enabled: false,
      report: { benchmark_path: [{ adapter: 'mihomo', interface: 'en0', target: '1.1.1.1', guard_applied: true, socket_bound: true, proxy_observed: true, direct_verified: true, physical_route_used: false, verification: 'mihomo_connection_direct' }] },
    } as QuickStartResult;
    expect(describeQuickStartResult(result)).toContain('mihomo 确认为 DIRECT');
    result.report.benchmark_path = [{ ...result.report.benchmark_path![0], proxy_observed: false, physical_route_used: true }];
    expect(describeQuickStartResult(result)).toContain('绑定 en0');
    expect(describeQuickStartResult({ ...result, error: '手动域名未生效' })).toBe('手动域名未生效');
  });
});
