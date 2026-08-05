import { ActionIcon, Alert, Button, Checkbox, Group, SegmentedControl, SimpleGrid, Stack, Text, TextInput, Tooltip } from '@mantine/core';
import { useMemo, useState } from 'react';
import type { ColumnDef } from '@tanstack/react-table';
import ReactECharts from 'echarts-for-react';
import { Filter, Play, RotateCcw, Square } from 'lucide-react';
import type { BenchmarkResult } from '../api/types';
import { useConfig, useLatestBenchmark, useStatus } from '../api/hooks';
import { selectBenchmarkSnapshot, selectTopBenchmarkResults } from '../lib/benchmarkSnapshot';
import { formatDate, formatDuration, formatMbps, formatPercent, formatScore } from '../lib/format';
import { shouldApplyPolicy } from '../lib/policyAvailability';
import { useUIStore } from '../state/ui';
import { DataTable } from '../components/DataTable';
import { EmptyState, Metric, PageHeader, Section } from '../components/Page';
import { StatusBadge } from '../components/StatusBadge';
import { useRun } from '../hooks/useRun';

export function BenchmarkPage() {
  const run = useRun();
  const status = useStatus();
  const config = useConfig();
  const latestBenchmark = useLatestBenchmark(status.data?.state.last_ended_at);
  const filter = useUIStore((state) => state.benchmarkFilter);
  const setFilter = useUIStore((state) => state.setBenchmarkFilter);
  const [family, setFamily] = useState('all');
  const [forceRefresh, setForceRefresh] = useState(false);
  const [policyRequested, setPolicyRequested] = useState(true);
  const policyAvailable = Boolean(status.data?.policy_available);
  const applyPolicy = shouldApplyPolicy(policyAvailable, policyRequested);
  const snapshot = useMemo(() => selectBenchmarkSnapshot(run.report, latestBenchmark.data), [latestBenchmark.data, run.report]);
  const downloadTop = config.data?.benchmark.download_top ?? 20;
  const topResults = useMemo(() => selectTopBenchmarkResults(snapshot?.results, downloadTop), [downloadTop, snapshot?.results]);
  const rows = useMemo(() => topResults.filter((item) => (family === 'all' || item.family === Number(family)) && item.ip.toLowerCase().includes(filter.toLowerCase())), [family, filter, topResults]);
  const columns = useMemo<ColumnDef<BenchmarkResult>[]>(() => [
    { id: 'rank', header: '#', size: 44, enableSorting: false, cell: ({ row }) => <Text c="dimmed" className="tabular">{row.index + 1}</Text> },
    { accessorKey: 'ip', header: 'IP', size: 228, cell: ({ getValue }) => <Text ff="monospace" size="sm">{String(getValue())}</Text> },
    { accessorKey: 'family', header: '协议', size: 62, cell: ({ getValue }) => `IPv${getValue<number>()}` },
    { id: 'success', header: 'TCP', size: 78, accessorFn: (row) => row.successes / row.attempts, cell: ({ getValue }) => formatPercent(getValue<number>()) },
    { accessorKey: 'avg_latency', header: '平均延迟', size: 100, cell: ({ getValue }) => <span className="tabular">{formatDuration(getValue<number>())}</span> },
    { accessorKey: 'p95_latency', header: 'P95', size: 88, cell: ({ getValue }) => <span className="tabular">{formatDuration(getValue<number>())}</span> },
    { accessorKey: 'jitter', header: '抖动', size: 82, cell: ({ getValue }) => <span className="tabular">{formatDuration(getValue<number>())}</span> },
    { accessorKey: 'mbps', header: '吞吐', size: 108, cell: ({ getValue }) => <span className="tabular">{formatMbps(getValue<number>())}</span> },
    { accessorKey: 'score', header: '评分', size: 72, cell: ({ getValue }) => <Text fw={650} className="tabular">{formatScore(getValue<number>())}</Text> },
    { id: 'state', header: '结果', size: 90, accessorFn: (row) => row.qualified, cell: ({ row }) => <StatusBadge label={row.original.qualified ? row.original.tls_verified ? '已验证' : '合格' : '已淘汰'} tone={row.original.tls_verified ? 'verified' : row.original.qualified ? 'pending' : 'neutral'} /> },
  ], []);
  const chart = useMemo(() => ({
    animation: false,
    grid: { left: 52, right: 52, top: 20, bottom: 56 },
    tooltip: { trigger: 'axis' },
    xAxis: { type: 'category', data: rows.slice(0, 10).map((row) => row.ip), axisLabel: { rotate: 28, hideOverlap: true, formatter: (value: string) => value.length > 16 ? `${value.slice(0, 12)}…` : value } },
    yAxis: [{ type: 'value', name: '评分', max: 100 }, { type: 'value', name: 'Mbps' }],
    series: [{ name: '评分', type: 'bar', data: rows.slice(0, 10).map((row) => row.score), itemStyle: { color: '#1677a6' } }, { name: '吞吐', type: 'line', yAxisIndex: 1, data: rows.slice(0, 10).map((row) => row.mbps ?? 0), itemStyle: { color: '#c47a12' } }],
  }), [rows]);
  const progress = run.event?.progress;
  const qualified = topResults.filter((item) => item.qualified).length;
  const currentPolicyVerified = Boolean(status.data?.state.current_ipv4?.policy_verified || status.data?.state.current_ipv6?.policy_verified);
  const reportMatchesSnapshot = Boolean(run.report && snapshot?.runId === run.report.id);
  const verifiedPolicy = Boolean((reportMatchesSnapshot && run.report?.policy_applied) || currentPolicyVerified);
  return (
    <Stack gap="lg">
      <PageHeader title="测速优选" description="两阶段候选测速、稳定选择与策略应用" actions={<Group gap="xs"><Button leftSection={<Play size={16} />} disabled={run.running} onClick={() => run.run({ force_range_refresh: forceRefresh, apply_policy: applyPolicy })}>开始优选</Button><Tooltip label="取消当前任务"><ActionIcon color="red" aria-label="取消当前任务" disabled={!run.running} loading={run.cancelling} onClick={run.cancel}><Square size={15} fill="currentColor" /></ActionIcon></Tooltip></Group>} />
      <Section className="toolbar-section">
        <Group justify="space-between" align="flex-end">
          <Group align="flex-end">
            <TextInput label="筛选候选" leftSection={<Filter size={15} />} placeholder="IP 地址" value={filter} onChange={(event) => setFilter(event.currentTarget.value)} />
            <div><Text size="xs" fw={500} mb={5}>显示协议</Text><SegmentedControl value={family} onChange={setFamily} data={[{ label: '双栈', value: 'all' }, { label: 'IPv4', value: '4' }, { label: 'IPv6', value: '6' }]} /></div>
          </Group>
          <Group><Checkbox label="强制刷新网段" checked={forceRefresh} onChange={(event) => setForceRefresh(event.currentTarget.checked)} /><Checkbox label="应用并验证策略" checked={applyPolicy} disabled={!policyAvailable} onChange={(event) => setPolicyRequested(event.currentTarget.checked)} /></Group>
        </Group>
      </Section>
      {run.error && <Alert color={run.error.message.includes('cancelled') ? 'gray' : 'red'} title={run.error.message.includes('cancelled') ? '任务已取消' : '任务失败'}>{run.error.message}</Alert>}
      {latestBenchmark.isError && <Alert color="yellow" title="最近结果刷新失败"><Group justify="space-between" align="center" wrap="nowrap"><Text size="sm">{latestBenchmark.error.message}</Text><Button size="compact-xs" variant="subtle" loading={latestBenchmark.isFetching} onClick={() => void latestBenchmark.refetch()}>重试</Button></Group></Alert>}
      <SimpleGrid cols={{ base: 2, md: 4 }} spacing="sm">
        <Metric label="阶段" value={run.event?.stage ?? (run.running ? '启动中' : snapshot ? '已完成' : '等待开始')} detail={run.event?.message ?? (snapshot ? `最近结果 ${formatDate(snapshot.finishedAt)}` : undefined)} accent="#1677a6" />
        <Metric label="进度" value={progress ? `${progress.completed} / ${progress.total}` : snapshot ? '已保存' : '—'} detail={progress ? `合格 ${progress.qualified}` : snapshot ? `合格 ${qualified}` : '尚无活动任务'} />
        <Metric label="候选结果" value={topResults.length} detail={`前 ${downloadTop} 名 · 当前显示 ${rows.length}`} />
        <Metric label="策略" value={verifiedPolicy ? '已验证' : !policyAvailable ? '不可用' : applyPolicy ? '完成后应用' : '仅测速'} detail={verifiedPolicy ? '当前节点策略已有验证证据' : !policyAvailable ? '当前运行时没有已启用适配器' : '应用前不会修改系统'} accent={verifiedPolicy ? '#2b8a5a' : '#75808a'} />
      </SimpleGrid>
      <Section title={`候选结果（前 ${downloadTop} 名）`} aside={snapshot && <Button variant="subtle" leftSection={<RotateCcw size={15} />} onClick={() => run.run({ force_range_refresh: false, apply_policy: false })}>仅复测</Button>}>
        {run.running && !snapshot ? <div className="running-placeholder"><div className="pulse-line" /><Text fw={600}>后台正在生成候选结果</Text><Text size="sm" c="dimmed">关闭此窗口不会停止任务；重新打开后会恢复当前阶段。</Text></div> : <DataTable columns={columns} data={rows} emptyTitle="尚无候选结果" emptyDetail="开始一次优选后，TCP、TLS 与下载指标会显示在这里。" minWidth={980} rowKey={(row) => row.ip} />}
      </Section>
      <Section title="前十名评分与吞吐">
        {rows.length ? <ReactECharts option={chart} style={{ height: 300 }} opts={{ renderer: 'svg' }} /> : <EmptyState title="暂无图表数据" detail="完整优选结束后显示前十名比较。" />}
      </Section>
    </Stack>
  );
}
