import { type ReactNode, useEffect, useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { notifications } from '@mantine/notifications';
import { request, subscribeOptimizerEvents } from '../api/client';
import { queryKeys } from '../api/hooks';
import type { OptimizerEvent, QuickStartResult, RunReport, SystemStatus } from '../api/types';
import { RunContext, type QuickStartRunOptions, type RunOptions } from '../state/run';

/** RunProvider 统一管理普通测速与快速流程共享的事件、取消和最终状态。 */
export function RunProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const [event, setEvent] = useState<OptimizerEvent>();
  const [report, setReport] = useState<RunReport>();
  const [quickStartResult, setQuickStartResult] = useState<QuickStartResult>();
  const [runError, setRunError] = useState<Error>();
  useEffect(() => subscribeOptimizerEvents((next) => {
    if (next.type === 'run.finished') {
      setEvent(undefined);
      queryClient.setQueryData<SystemStatus>(queryKeys.status, (current) => current ? { ...current, active_event: undefined, state: { ...current.state, running: false } } : current);
      return;
    }
    setEvent(next);
    queryClient.setQueryData<SystemStatus>(queryKeys.status, (current) => current ? { ...current, active_event: next, state: { ...current.state, running: true } } : current);
  }), [queryClient]);

  const runMutation = useMutation({
    mutationFn: (options: RunOptions) => request<RunReport>('optimizer.run', options),
    onMutate: () => { setEvent(undefined); setQuickStartResult(undefined); setRunError(undefined); },
    onSuccess: (nextReport) => {
      setReport(nextReport);
      notifications.show({ color: nextReport.warnings?.length ? 'yellow' : 'green', title: nextReport.policy_applied ? '已验证' : '仅测速完成', message: nextReport.policy_applied ? '后台已完成策略和选路验证' : '测速结果已生成，未修改系统策略' });
    },
    onError: (error: Error) => { setRunError(error); notifications.show({ color: error.message.includes('cancelled') ? 'gray' : 'red', title: error.message.includes('cancelled') ? '优选已取消' : '优选失败', message: error.message }); },
    onSettled: async () => {
      setEvent(undefined);
      await Promise.all([queryClient.invalidateQueries({ queryKey: queryKeys.status }), queryClient.invalidateQueries({ queryKey: queryKeys.history }), queryClient.invalidateQueries({ queryKey: queryKeys.routes })]);
    },
  });
  const quickStartMutation = useMutation({
    mutationFn: (options: QuickStartRunOptions) => request<QuickStartResult>('quickstart.run', options),
    onMutate: () => { setEvent(undefined); setQuickStartResult(undefined); setRunError(undefined); },
    onSuccess: (result) => {
      setReport(result.report);
      setQuickStartResult(result);
      const presentation = result.status === 'verified'
        ? { color: 'green', title: '已验证', message: result.auto_maintenance_enabled ? '本次策略已验证，并已启用以后自动维护' : '本次策略和实际选路已经验证' }
        : result.status === 'rolled_back'
          ? { color: 'gray', title: '已回滚', message: result.error || '策略未通过验证，后台已恢复修改前状态' }
          : { color: 'yellow', title: '部分完成', message: result.persistence_warning || result.error || '请检查运行证据和高级诊断' };
      notifications.show(presentation);
    },
    onError: (error: Error) => { setRunError(error); notifications.show({ color: error.message.includes('cancelled') ? 'gray' : 'red', title: error.message.includes('cancelled') ? '优选已取消' : '一键优选未完成', message: error.message }); },
    onSettled: async () => {
      setEvent(undefined);
      await Promise.all([queryClient.invalidateQueries({ queryKey: queryKeys.status }), queryClient.invalidateQueries({ queryKey: queryKeys.history }), queryClient.invalidateQueries({ queryKey: queryKeys.routes }), queryClient.invalidateQueries({ queryKey: queryKeys.config })]);
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
    <RunContext.Provider value={{ event, report, quickStartResult, running: runMutation.isPending || quickStartMutation.isPending, cancelling: cancelMutation.isPending, error: runError, run: runMutation.mutateAsync, runQuickStart: quickStartMutation.mutateAsync, cancel: cancelMutation.mutate }}>
      {children}
    </RunContext.Provider>
  );
}
