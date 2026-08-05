import { QueryClient } from '@tanstack/react-query';
import { describe, expect, it } from 'vitest';
import { queryKeys } from './hooks';
import { invalidateRunCompletionQueries } from './runCompletion';

describe('invalidateRunCompletionQueries', () => {
  it('任务结束后同步失效状态、历史、路由和域名映射', async () => {
    const client = new QueryClient();
    const expectedKeys = [
      queryKeys.status,
      queryKeys.history,
      queryKeys.latestBenchmark,
      queryKeys.routes,
      queryKeys.accelerationDomains,
    ];
    for (const queryKey of expectedKeys) client.setQueryData(queryKey, {});

    await invalidateRunCompletionQueries(client);

    for (const queryKey of expectedKeys) expect(client.getQueryState(queryKey)?.isInvalidated).toBe(true);
  });

  it('快速流程结束后额外失效配置', async () => {
    const client = new QueryClient();
    client.setQueryData(queryKeys.config, {});

    await invalidateRunCompletionQueries(client, { includeConfig: true });

    expect(client.getQueryState(queryKeys.config)?.isInvalidated).toBe(true);
  });
});
