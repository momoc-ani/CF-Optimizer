import { useQuery } from '@tanstack/react-query';
import { request } from './client';
import type { AppConfig, DomainDiscoveryResult, LatestBenchmark, ProxyDetections, RangeSnapshot, RouteTransaction, RunSummary, SystemStatus } from './types';

export const queryKeys = {
  status: ['system', 'status'] as const,
  ranges: ['ranges'] as const,
  routes: ['routes'] as const,
  proxies: ['proxies'] as const,
  history: ['history'] as const,
  latestBenchmark: ['benchmark', 'latest'] as const,
  logs: ['logs'] as const,
  config: ['config'] as const,
  accelerationDomains: ['acceleration', 'domains'] as const,
};

export function useStatus() {
  return useQuery({ queryKey: queryKeys.status, queryFn: () => request<SystemStatus>('system.status'), refetchInterval: 2_000 });
}

export function useRanges() {
  return useQuery({ queryKey: queryKeys.ranges, queryFn: () => request<RangeSnapshot>('ranges.get') });
}

export function useRoutes() {
  return useQuery({ queryKey: queryKeys.routes, queryFn: () => request<RouteTransaction[]>('routes.list') });
}

export function useProxies() {
  return useQuery({
    queryKey: queryKeys.proxies,
    queryFn: () => request<ProxyDetections>('proxy.detect'),
    refetchInterval: 5_000,
  });
}

export function useHistory() {
  return useQuery({ queryKey: queryKeys.history, queryFn: () => request<RunSummary[]>('history.list') });
}

/** useLatestBenchmark 以后台结束时间作为版本信号，任务完成后自动读取最新成功明细。 */
export function useLatestBenchmark(lastEndedAt?: string) {
  return useQuery({
    queryKey: [...queryKeys.latestBenchmark, lastEndedAt ?? 'none'],
    queryFn: () => request<LatestBenchmark>('history.latest'),
  });
}

export function useLogs(lines = 500) {
  return useQuery({ queryKey: [...queryKeys.logs, lines], queryFn: () => request<string[]>('logs.tail', { lines }), refetchInterval: 5_000 });
}

export function useConfig() {
  return useQuery({ queryKey: queryKeys.config, queryFn: () => request<AppConfig>('config.get') });
}

export function useAccelerationDomains() {
  return useQuery({
    queryKey: queryKeys.accelerationDomains,
    queryFn: () => request<DomainDiscoveryResult>('acceleration.domains'),
    refetchInterval: 5_000,
  });
}
