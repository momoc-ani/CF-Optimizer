import type { QueryClient, QueryKey } from '@tanstack/react-query';
import { queryKeys } from './hooks';

const runCompletionQueryKeys = [
  queryKeys.status,
  queryKeys.history,
  queryKeys.latestBenchmark,
  queryKeys.routes,
  queryKeys.accelerationDomains,
] satisfies QueryKey[];

/** invalidateRunCompletionQueries 让任务结果、当前策略及域名映射在同一完成点刷新。 */
export async function invalidateRunCompletionQueries(
  queryClient: QueryClient,
  options: { includeConfig?: boolean } = {},
): Promise<void> {
  const keys: QueryKey[] = options.includeConfig
    ? [...runCompletionQueryKeys, queryKeys.config]
    : runCompletionQueryKeys;
  await Promise.all(keys.map((queryKey) => queryClient.invalidateQueries({ queryKey })));
}
