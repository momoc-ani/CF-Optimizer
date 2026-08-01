import { Alert, Button, Group, SimpleGrid, Stack, Text, ThemeIcon } from '@mantine/core';
import { useMemo } from 'react';
import ReactECharts from 'echarts-for-react';
import { Activity, Cable, Clock3, Network, Play, Route, ShieldCheck } from 'lucide-react';
import { useHistory, useProxies, useRanges, useRoutes, useStatus } from '../api/hooks';
import type { Selection } from '../api/types';
import { formatDate, formatScore } from '../lib/format';
import { EmptyState, LoadingState, Metric, PageHeader, Section } from '../components/Page';
import { StatusBadge } from '../components/StatusBadge';
import { useRun } from '../hooks/useRun';

function SelectionRow({ title, selection, interfaceName }: { title: string; selection?: Selection; interfaceName?: string }) {
  if (!selection) return <div className="selection-row"><Text fw={650}>{title}</Text><Text c="dimmed" size="sm">尚未选择节点</Text><StatusBadge label="未验证" /></div>;
  return (
    <div className="selection-row">
      <div><Text size="xs" c="dimmed">{title}</Text><Text fw={650} ff="monospace">{selection.ip}</Text></div>
      <div><Text size="xs" c="dimmed">评分</Text><Text className="tabular" fw={650}>{formatScore(selection.score)}</Text></div>
      <div><Text size="xs" c="dimmed">物理出口</Text><Text>{interfaceName || '未发现'}</Text></div>
      <div><Text size="xs" c="dimmed">最近验证</Text><Text>{formatDate(selection.last_successful_at)}</Text></div>
      <StatusBadge label={selection.policy_verified ? '策略已验证' : '策略未验证'} tone={selection.policy_verified ? 'verified' : 'warning'} />
    </div>
  );
}

export function OverviewPage() {
  const status = useStatus();
  const routes = useRoutes();
  const proxies = useProxies();
  const ranges = useRanges();
  const history = useHistory();
  const run = useRun();
  const chart = useMemo(() => {
    const records = [...(history.data ?? [])].reverse();
    return {
      animation: false,
      grid: { left: 42, right: 52, top: 24, bottom: 28 },
      tooltip: { trigger: 'axis' },
      legend: { data: ['评分', '延迟'], top: 0, textStyle: { color: 'var(--mantine-color-dimmed)' } },
      xAxis: { type: 'category', data: records.map((item) => formatDate(item.started_at).slice(0, 5)), axisLabel: { color: 'var(--mantine-color-dimmed)' } },
      yAxis: [{ type: 'value', name: '评分', min: 0, max: 100 }, { type: 'value', name: 'ms' }],
      series: [
        { name: '评分', type: 'line', smooth: true, data: records.map((item) => item.best?.[0]?.score ?? null), itemStyle: { color: '#1677a6' }, areaStyle: { color: 'rgba(22,119,166,.08)' } },
        { name: '延迟', type: 'line', yAxisIndex: 1, data: records.map((item) => item.best?.[0] ? item.best[0].avg_latency / 1_000_000 : null), itemStyle: { color: '#c47a12' } },
      ],
    };
  }, [history.data]);
  if (status.isLoading) return <LoadingState rows={7} />;
  const state = status.data?.state;
  const path = status.data?.physical_path;
  const detected = Object.values(proxies.data ?? {}).filter((item) => item.present).length;
  const verifiedRoutes = (routes.data ?? []).filter((item) => item.state === 'verified').length;
  return (
    <Stack gap="lg">
      <PageHeader title="总览" description="后台服务、当前节点与已验证直连策略" actions={<Button leftSection={<Play size={16} />} loading={run.running} onClick={() => run.run({ force_range_refresh: false, apply_policy: true })}>立即优选</Button>} />
      {state?.last_error && <Alert color="red" title="上次任务失败">{state.last_error}</Alert>}
      <SimpleGrid cols={{ base: 2, md: 4 }} spacing="sm">
        <Metric label="后台服务" value={<Group gap={7}><ThemeIcon size="sm" color={state ? 'green' : 'red'} variant="light"><Activity size={14} /></ThemeIcon>{state?.running ? '优选运行中' : '运行正常'}</Group>} detail={`协议 v${status.data?.protocol_version ?? '—'}`} accent="#2b8a5a" />
        <Metric label="下次周期" value="约 5 小时 54 分" detail="周期 6 小时 · 网络变化触发" accent="#1677a6" />
        <Metric label="代理适配" value={`${detected} 个已检测`} detail="仅显示可验证的适配器状态" accent="#7a59a8" />
        <Metric label="Cloudflare 网段" value={`${(ranges.data?.ipv4.length ?? 0) + (ranges.data?.ipv6.length ?? 0)} 条`} detail={`${ranges.data?.source ?? '读取中'} · ${formatDate(ranges.data?.fetched_at)}`} accent="#c47a12" />
      </SimpleGrid>

      <Section title="当前优选节点" aside={<StatusBadge label={verifiedRoutes > 0 ? `${verifiedRoutes} 条路由已验证` : '无已验证路由'} tone={verifiedRoutes > 0 ? 'verified' : 'warning'} />}>
        <Stack gap={0} className="selection-list">
          <SelectionRow title="IPv4" selection={state?.current_ipv4} interfaceName={path?.interface} />
          <SelectionRow title="IPv6" selection={state?.current_ipv6} interfaceName={path?.interface} />
        </Stack>
      </Section>

      <div className="overview-grid">
        <Section title="近 24 小时趋势">
          {(history.data?.length ?? 0) > 0 ? <ReactECharts option={chart} style={{ height: 236 }} opts={{ renderer: 'svg' }} /> : <EmptyState title="暂无趋势" detail="完成首次优选后显示评分和延迟趋势。" />}
        </Section>
        <Section title="运行证据">
          <Stack gap="xs" className="evidence-list">
            <Group justify="space-between"><Group gap="xs"><Network size={16} /><Text size="sm">物理接口</Text></Group><Text size="sm" ff="monospace">{path?.interface ?? '未发现'}</Text></Group>
            <Group justify="space-between"><Group gap="xs"><Route size={16} /><Text size="sm">IPv4 网关</Text></Group><Text size="sm" ff="monospace">{path?.gateway_ipv4 ?? '—'}</Text></Group>
            <Group justify="space-between"><Group gap="xs"><Cable size={16} /><Text size="sm">代理内核</Text></Group><Text size="sm">{detected} 个已检测</Text></Group>
            <Group justify="space-between"><Group gap="xs"><ShieldCheck size={16} /><Text size="sm">策略证据</Text></Group><StatusBadge label={verifiedRoutes ? '已验证' : '待验证'} tone={verifiedRoutes ? 'verified' : 'pending'} /></Group>
            <Group justify="space-between"><Group gap="xs"><Clock3 size={16} /><Text size="sm">上次完成</Text></Group><Text size="sm">{formatDate(state?.last_ended_at)}</Text></Group>
          </Stack>
        </Section>
      </div>

      <Section title="最近事件">
        {(history.data ?? []).slice(0, 4).map((item) => <div className="event-row" key={item.id}><span className={`event-severity ${item.error ? 'warning' : 'success'}`} /><Text size="sm" className="event-time tabular">{formatDate(item.finished_at)}</Text><Text size="sm" fw={550}>{item.error ? '优选部分完成' : '优选任务完成'}</Text><Text size="sm" c="dimmed" lineClamp={1}>{item.switch_reason || '未发生节点切换'}</Text></div>)}
      </Section>
    </Stack>
  );
}
