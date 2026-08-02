import { describe, expect, it } from 'vitest';
import type { RunSummary } from '../api/types';
import { formatRunEventDetail, formatRunEventTitle, presentSchedule } from './overview';

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
});
