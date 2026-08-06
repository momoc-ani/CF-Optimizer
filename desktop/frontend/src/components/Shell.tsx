import type { ReactNode } from 'react';
import { ActionIcon, Group, Text, Tooltip, useMantineColorScheme } from '@mantine/core';
import { Activity, Cable, ChartNoAxesCombined, Gauge, Globe2, History, ListTree, Moon, Network, Route, Settings, Sun, TestTubeDiagonal } from 'lucide-react';
import type { SystemStatus } from '../api/types';
import { type PageKey, useUIStore } from '../state/ui';
import { StatusBadge } from './StatusBadge';

const navigation: Array<{ key: PageKey; label: string; icon: typeof Gauge }> = [
  { key: 'overview', label: '总览', icon: Gauge },
  { key: 'benchmark', label: '测速优选', icon: Activity },
  { key: 'acceleration', label: '域名加速', icon: Globe2 },
  { key: 'proxy', label: '代理适配', icon: Cable },
  { key: 'routes', label: '网络路由', icon: Route },
  { key: 'ranges', label: '网段管理', icon: Network },
  { key: 'history', label: '历史记录', icon: History },
  { key: 'logs', label: '日志诊断', icon: ListTree },
  { key: 'settings', label: '设置', icon: Settings },
];

export function Shell({ status, disconnected, taskStrip, children }: { status?: SystemStatus; disconnected: boolean; taskStrip?: ReactNode; children: ReactNode }) {
  const page = useUIStore((state) => state.page);
  const setPage = useUIStore((state) => state.setPage);
  const { colorScheme, setColorScheme } = useMantineColorScheme();
  const isBusy = Boolean(status?.state.running || status?.active_event);
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <button className="brand" onClick={() => setPage('overview')} aria-label="CF Optimizer 总览">
          <span className="brand-mark"><ChartNoAxesCombined size={21} /></span>
          <span className="brand-copy"><strong>CF Optimizer</strong><small>Direct route console</small></span>
        </button>
        <nav aria-label="主导航">
          {navigation.map((item) => {
            const Icon = item.icon;
            return <Tooltip key={item.key} label={item.label} position="right" disabled={false}><button className={`nav-item ${page === item.key ? 'active' : ''}`} aria-label={item.label} aria-current={page === item.key ? 'page' : undefined} onClick={() => setPage(item.key)}><Icon size={18} /><span>{item.label}</span></button></Tooltip>;
          })}
        </nav>
        <div className="sidebar-footer">
          <StatusBadge label={disconnected ? '服务未连接' : isBusy ? '后台任务运行中' : '服务已连接'} tone={disconnected ? 'failed' : isBusy ? 'pending' : 'verified'} />
          <Text size="xs" c="dimmed" className="sidebar-version">v{status?.build.version ?? '—'}</Text>
        </div>
      </aside>
      <div className="workspace">
        <header className="topbar">
          <Group gap="xs"><TestTubeDiagonal size={16} /><Text size="sm" c="dimmed">后台协议 v{status?.protocol_version ?? '—'}</Text></Group>
          <Group gap="xs">
            <StatusBadge label="普通权限 UI" tone="neutral" />
            <Tooltip label={colorScheme === 'dark' ? '切换浅色主题' : '切换深色主题'}>
              <ActionIcon aria-label="切换主题" onClick={() => setColorScheme(colorScheme === 'dark' ? 'light' : 'dark')}>{colorScheme === 'dark' ? <Sun size={17} /> : <Moon size={17} />}</ActionIcon>
            </Tooltip>
          </Group>
        </header>
        {taskStrip}
        <main className="workspace-content">{children}</main>
      </div>
    </div>
  );
}
