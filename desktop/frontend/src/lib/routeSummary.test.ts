import { describe, expect, it } from 'vitest';
import type { RouteTransaction, Selection, ServiceState } from '../api/types';
import { countCurrentVerifiedRoutes, countTemporaryRoutesRequiringCleanup } from './routeSummary';

const selection = (policyVerified: boolean): Selection => ({
  ip: '104.17.158.152',
  family: 4,
  score: 92,
  selected_at: '2026-08-01T23:07:53Z',
  last_successful_at: '2026-08-01T23:07:53Z',
  consecutive_failures: 0,
  policy_verified: policyVerified,
});

const serviceState = (overrides: Partial<ServiceState>): ServiceState => ({
  version: 1,
  updated_at: '2026-08-01T23:08:18Z',
  history: [],
  running: false,
  ...overrides,
});

const transaction = (state: string, temporary = true): RouteTransaction => ({
  id: `route-${state}`,
  operation: 'replace',
  route: {
    prefix: '104.16.0.0/13',
    gateway: '192.168.15.1',
    interface: '以太网 3',
    metric: 5,
  },
  temporary,
  state,
  started_at: '2026-08-01T23:07:53Z',
  updated_at: '2026-08-01T23:08:18Z',
});

describe('routeSummary', () => {
  it('只统计当前选择中已经验证的路由', () => {
    expect(countCurrentVerifiedRoutes(serviceState({
      current_ipv4: selection(true),
      current_ipv6: { ...selection(false), family: 6, ip: '2606:4700::1' },
    }))).toBe(1);
  });

  it('没有当前选择时不复用历史验证数量', () => {
    expect(countCurrentVerifiedRoutes(serviceState({}))).toBe(0);
  });

  it('已恢复和已回滚事务不再计入待清理临时路由', () => {
    const rows = [
      transaction('planned'),
      transaction('applied'),
      transaction('verified'),
      transaction('rollback_failed'),
      transaction('recovery_failed'),
      transaction('recovered'),
      transaction('rolled_back'),
      transaction('apply_failed'),
      transaction('verified', false),
    ];

    expect(countTemporaryRoutesRequiringCleanup(rows)).toBe(5);
  });
});
