import type { BenchmarkResult, LatestBenchmark, RunReport } from '../api/types';

/** BenchmarkSnapshot 是测速页当前应展示的稳定结果快照。 */
export interface BenchmarkSnapshot {
  runId: string;
  finishedAt: string;
  results: BenchmarkResult[];
  source: 'live' | 'persisted';
}

/** selectTopBenchmarkResults 按后台已经确定的评分顺序截取 download_top 个结果。 */
export function selectTopBenchmarkResults(results: BenchmarkResult[] | undefined, limit: number): BenchmarkResult[] {
  if (!results?.length || !Number.isFinite(limit)) return [];
  const count = Math.floor(limit);
  return count > 0 ? results.slice(0, count) : [];
}

function timestamp(value: string): number {
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? 0 : parsed;
}

/** selectBenchmarkSnapshot 在内存报告与后台明细之间选择完成时间较新的结果。 */
export function selectBenchmarkSnapshot(report?: RunReport, persisted?: LatestBenchmark): BenchmarkSnapshot | undefined {
  const hasPersisted = Boolean(persisted?.run_id);
  if (!report && !hasPersisted) return undefined;
  if (report && (!hasPersisted || timestamp(report.finished_at) >= timestamp(persisted!.finished_at))) {
    return { runId: report.id, finishedAt: report.finished_at, results: report.results, source: 'live' };
  }
  return { runId: persisted!.run_id, finishedAt: persisted!.finished_at, results: persisted!.results, source: 'persisted' };
}
