import { Alert, Button } from '@mantine/core';
import { PlugZap } from 'lucide-react';
import { lazy, Suspense } from 'react';
import { useStatus } from './api/hooks';
import { RunProvider } from './components/RunContext';
import { Shell } from './components/Shell';
import { TaskStrip } from './components/TaskStrip';
import { LoadingState } from './components/Page';
import { PolicyGuardNotifications } from './components/PolicyGuardNotifications';
import { useUIStore } from './state/ui';
import { useRun } from './hooks/useRun';
import { selectActiveTaskEvent } from './lib/activeTask';

const OverviewPage = lazy(() => import('./pages/OverviewPage').then((module) => ({ default: module.OverviewPage })));
const BenchmarkPage = lazy(() => import('./pages/BenchmarkPage').then((module) => ({ default: module.BenchmarkPage })));
const AccelerationPage = lazy(() => import('./pages/AccelerationPage').then((module) => ({ default: module.AccelerationPage })));
const ProxyPage = lazy(() => import('./pages/ProxyPage').then((module) => ({ default: module.ProxyPage })));
const RoutesPage = lazy(() => import('./pages/RoutesPage').then((module) => ({ default: module.RoutesPage })));
const RangesPage = lazy(() => import('./pages/RangesPage').then((module) => ({ default: module.RangesPage })));
const HistoryPage = lazy(() => import('./pages/HistoryPage').then((module) => ({ default: module.HistoryPage })));
const LogsPage = lazy(() => import('./pages/LogsPage').then((module) => ({ default: module.LogsPage })));
const SettingsPage = lazy(() => import('./pages/SettingsPage').then((module) => ({ default: module.SettingsPage })));

const pages = { overview: OverviewPage, benchmark: BenchmarkPage, acceleration: AccelerationPage, proxy: ProxyPage, routes: RoutesPage, ranges: RangesPage, history: HistoryPage, logs: LogsPage, settings: SettingsPage };

function Workspace() {
  const status = useStatus();
  const page = useUIStore((state) => state.page);
  const run = useRun();
  const CurrentPage = pages[page];
  const activeEvent = selectActiveTaskEvent(run.running, run.event, status.data);
  const startup = status.data?.startup;
  const isRecovering = Boolean(startup && !startup.ready);
  const recoveryProgress = startup?.progress && startup.progress.total > 0 ? ` (${startup.progress.completed}/${startup.progress.total})` : '';
  return (
    <Shell status={status.data} disconnected={status.isError} taskStrip={<TaskStrip event={activeEvent} cancelling={run.cancelling} onCancel={run.cancel} />}>
      <PolicyGuardNotifications guards={status.data?.policy_guards} />
      {status.isError && (
        <Alert mb="md" color="red" icon={<PlugZap size={18} />} title="无法连接后台服务">
          <span>{status.error.message}</span>
          <Button ml="md" size="compact-xs" variant="light" color="red" onClick={() => status.refetch()}>重试</Button>
        </Alert>
      )}
      {isRecovering && (
        <Alert mb="md" color="yellow" icon={<PlugZap size={18} />} title="后台服务恢复中">
          {startup?.message ?? '正在恢复中断的系统事务，请稍后重试业务操作。'}{recoveryProgress}
        </Alert>
      )}
      <Suspense fallback={<LoadingState rows={7} />}><CurrentPage /></Suspense>
    </Shell>
  );
}

export function App() {
  return <RunProvider><Workspace /></RunProvider>;
}
