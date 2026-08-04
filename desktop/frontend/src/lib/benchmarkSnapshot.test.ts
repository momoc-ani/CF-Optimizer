import { describe, expect, it } from 'vitest';
import type { BenchmarkResult, LatestBenchmark, RunReport } from '../api/types';
import { selectBenchmarkSnapshot } from './benchmarkSnapshot';

const result = (ip: string, score: number): BenchmarkResult => ({
  ip,
  family: 4,
  attempts: 4,
  successes: 4,
  loss: 0,
  avg_latency: 50_000_000,
  p95_latency: 60_000_000,
  jitter: 2_000_000,
  tcp_qualified: true,
  tls_verified: true,
  download_verified: false,
  qualified: true,
  score,
});

const report = (id: string, finishedAt: string, item: BenchmarkResult): RunReport => ({
  id,
  started_at: '2026-08-05T00:00:00Z',
  finished_at: finishedAt,
  range_source: 'test',
  range_hash: 'hash',
  results: [item],
  ipv4_decision: { selected: item, changed: false, reason: 'test' },
  ipv6_decision: { changed: false, reason: 'disabled' },
  policy_applied: true,
});

describe('selectBenchmarkSnapshot', () => {
  it('应用重启后显示后台保存的最近成功结果', () => {
    const persisted: LatestBenchmark = {
      run_id: 'persisted',
      finished_at: '2026-08-05T00:10:00Z',
      saved_at: '2026-08-05T00:10:01Z',
      results: [result('104.25.250.104', 93.31)],
    };
    expect(selectBenchmarkSnapshot(undefined, persisted)).toEqual({
      runId: 'persisted',
      finishedAt: persisted.finished_at,
      results: persisted.results,
      source: 'persisted',
    });
  });

  it('查询刷新前先显示刚完成的一键优选结果', () => {
    const persisted: LatestBenchmark = {
      run_id: 'old',
      finished_at: '2026-08-05T00:10:00Z',
      saved_at: '2026-08-05T00:10:01Z',
      results: [result('104.25.241.29', 91.91)],
    };
    const live = report('new', '2026-08-05T00:20:00Z', result('104.25.250.104', 93.31));
    expect(selectBenchmarkSnapshot(live, persisted)?.runId).toBe('new');
  });

  it('后台任务完成后用更新的持久化结果替换旧内存结果', () => {
    const persisted: LatestBenchmark = {
      run_id: 'scheduled',
      finished_at: '2026-08-05T00:30:00Z',
      saved_at: '2026-08-05T00:30:01Z',
      results: [result('104.17.27.6', 94.2)],
    };
    const live = report('old', '2026-08-05T00:20:00Z', result('104.25.250.104', 93.31));
    expect(selectBenchmarkSnapshot(live, persisted)?.runId).toBe('scheduled');
  });
});
