import { type ReactNode, useEffect, useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { notifications } from '@mantine/notifications';
import { request, subscribeOptimizerEvents } from '../api/client';
import { queryKeys } from '../api/hooks';
import type { OptimizerEvent, RunReport, SystemStatus } from '../api/types';
import { RunContext, type RunOptions } from '../state/run';

export function RunProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const [event, setEvent] = useState<OptimizerEvent>();
  useEffect(() => subscribeOptimizerEvents((next) => {
    setEvent(next);
    queryClient.setQueryData<SystemStatus>(queryKeys.status, (current) => current ? { ...current, active_event: next, state: { ...current.state, running: true } } : current);
  }), [queryClient]);

  const runMutation = useMutation({
    mutationFn: (options: RunOptions) => request<RunReport>('optimizer.run', options),
    onSuccess: (report) => notifications.show({ color: report.warnings?.length ? 'yellow' : 'green', title: report.warnings?.length ? '优选部分完成' : '优选完成', message: report.policy_applied ? '策略已应用并由后台验证' : '测速结果已生成，未修改策略' }),
    onError: (error: Error) => notifications.show({ color: error.message.includes('cancelled') ? 'gray' : 'red', title: error.message.includes('cancelled') ? '优选已取消' : '优选失败', message: error.message }),
    onSettled: async () => {
      setEvent(undefined);
      await Promise.all([queryClient.invalidateQueries({ queryKey: queryKeys.status }), queryClient.invalidateQueries({ queryKey: queryKeys.history }), queryClient.invalidateQueries({ queryKey: queryKeys.routes })]);
    },
  });
  const cancelMutation = useMutation({
    mutationFn: () => request<{ cancelled: boolean }>('optimizer.cancel'),
    onSuccess: ({ cancelled }) => {
      if (!cancelled) notifications.show({ color: 'gray', message: '当前没有可取消的任务' });
    },
    onError: (error: Error) => notifications.show({ color: 'red', title: '取消失败', message: error.message }),
  });
  return (
    <RunContext.Provider value={{ event, report: runMutation.data, running: runMutation.isPending || Boolean(event), cancelling: cancelMutation.isPending, error: runMutation.error ?? undefined, run: runMutation.mutate, cancel: cancelMutation.mutate }}>
      {children}
    </RunContext.Provider>
  );
}
