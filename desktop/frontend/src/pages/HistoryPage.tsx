import { Button, Group, SegmentedControl, SimpleGrid, Stack, Text } from '@mantine/core';
import type { ColumnDef } from '@tanstack/react-table';
import ReactECharts from 'echarts-for-react';
import { History, RefreshCw } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useHistory } from '../api/hooks';
import type { RunSummary } from '../api/types';
import { DataTable } from '../components/DataTable';
import { ErrorState, LoadingState, Metric, PageHeader, Section } from '../components/Page';
import { StatusBadge } from '../components/StatusBadge';
import { formatDate, formatDuration, formatMbps, formatScore } from '../lib/format';

/** HistoryPage 展示历史优选摘要、趋势和单次运行决策。 */
export function HistoryPage() {
  const history = useHistory();
  const [filter, setFilter] = useState('all');
  const [selectedID, setSelectedID] = useState<string>();
  const rows = useMemo(() => (history.data ?? []).filter((item) => filter === 'all' || (filter === 'partial' ? Boolean(item.error) : !item.error)), [filter, history.data]);
  const selected = (history.data ?? []).find((item) => item.id === selectedID) ?? rows[0];
  const columns = useMemo<ColumnDef<RunSummary>[]>(() => [
    { accessorKey: 'started_at', header: '开始时间', size: 160, cell: ({ getValue }) => formatDate(String(getValue())) },
    { accessorKey: 'id', header: 'Run ID', size: 190, cell: ({ getValue }) => <Text ff="monospace" size="sm">{String(getValue())}</Text> },
    { accessorKey: 'candidates', header: '候选', size: 78 },
    { accessorKey: 'qualified', header: '合格', size: 72 },
    { accessorKey: 'selected_ipv4', header: 'IPv4', size: 160, cell: ({ getValue }) => <Text ff="monospace" size="sm">{String(getValue() ?? '—')}</Text> },
    { accessorKey: 'selected_ipv6', header: 'IPv6', size: 220, cell: ({ getValue }) => <Text ff="monospace" size="sm">{String(getValue() ?? '—')}</Text> },
    { id: 'state', header: '结果', size: 100, accessorFn: (row) => row.error, cell: ({ row }) => <StatusBadge label={row.original.error ? '部分完成' : '已完成'} tone={row.original.error ? 'warning' : 'verified'} /> },
  ], []);
  const chart = useMemo(() => {
    const ordered = [...rows].reverse();
    return {
      animation: false,
      grid: { left: 44, right: 18, top: 28, bottom: 32 },
      tooltip: { trigger: 'axis' },
      xAxis: { type: 'category', data: ordered.map((item) => formatDate(item.started_at).slice(0, 5)) },
      yAxis: { type: 'value', min: 0, max: 100, name: '评分' },
      series: [{ type: 'line', smooth: true, symbolSize: 7, data: ordered.map((item) => item.best?.[0]?.score ?? null), itemStyle: { color: '#1677a6' }, areaStyle: { color: 'rgba(22,119,166,.09)' } }],
    };
  }, [rows]);

  if (history.isLoading) return <LoadingState rows={7} />;
  if (history.isError) return <ErrorState message={history.error.message} onRetry={() => history.refetch()} />;
  const completed = (history.data ?? []).filter((item) => !item.error).length;
  const latestBest = history.data?.[0]?.best?.[0];
  return (
    <Stack gap="lg">
      <PageHeader title="历史记录" description="优选摘要、节点评分与切换原因" actions={<Button variant="light" leftSection={<RefreshCw size={16} />} loading={history.isFetching} onClick={() => history.refetch()}>刷新记录</Button>} />
      <SimpleGrid cols={{ base: 2, md: 4 }} spacing="sm">
        <Metric label="历史运行" value={history.data?.length ?? 0} detail={`${completed} 次完整完成`} accent="#1677a6" />
        <Metric label="最近合格" value={history.data?.[0]?.qualified ?? 0} detail={`候选 ${history.data?.[0]?.candidates ?? 0}`} accent="#2b8a5a" />
        <Metric label="最近评分" value={formatScore(latestBest?.score)} detail={formatDuration(latestBest?.avg_latency)} accent="#7950f2" />
        <Metric label="最近吞吐" value={formatMbps(latestBest?.mbps)} detail={latestBest?.ip ?? '无结果'} accent="#c47a12" />
      </SimpleGrid>
      <Section title="评分趋势" aside={<SegmentedControl size="xs" value={filter} onChange={setFilter} data={[{ label: '全部', value: 'all' }, { label: '已完成', value: 'complete' }, { label: '部分完成', value: 'partial' }]} />}>
        <ReactECharts option={chart} style={{ height: 230 }} opts={{ renderer: 'svg' }} />
      </Section>
      <div className="split-layout history-layout">
        <Section title="运行记录">
          <DataTable columns={columns} data={rows} minWidth={1020} rowKey={(row) => row.id} onRowClick={(row) => setSelectedID(row.id)} emptyTitle="暂无历史记录" emptyDetail="完成首次优选后会保存运行摘要。" />
        </Section>
        <Section title="运行详情" className="inspector-section">
          {selected ? (
            <Stack gap="md">
              <Group justify="space-between"><Group gap="xs"><History size={18} /><Text ff="monospace" fw={650}>{selected.id}</Text></Group><StatusBadge label={selected.error ? '部分完成' : '已完成'} tone={selected.error ? 'warning' : 'verified'} /></Group>
              <div className="property-grid">
                <Text c="dimmed">开始</Text><Text>{formatDate(selected.started_at)}</Text>
                <Text c="dimmed">结束</Text><Text>{formatDate(selected.finished_at)}</Text>
                <Text c="dimmed">IPv4</Text><Text ff="monospace">{selected.selected_ipv4 ?? '—'}</Text>
                <Text c="dimmed">IPv6</Text><Text ff="monospace">{selected.selected_ipv6 ?? '—'}</Text>
                <Text c="dimmed">切换原因</Text><Text>{selected.switch_reason ?? '未记录'}</Text>
                <Text c="dimmed">错误</Text><Text c={selected.error ? 'red' : undefined}>{selected.error ?? '无'}</Text>
              </div>
              {(selected.best ?? []).map((result, index) => <div className="rank-row" key={result.ip}><Text fw={650}>#{index + 1}</Text><Text ff="monospace" size="sm">{result.ip}</Text><Text className="tabular">{formatScore(result.score)}</Text></div>)}
            </Stack>
          ) : <Text c="dimmed">选择一次运行查看详情。</Text>}
        </Section>
      </div>
    </Stack>
  );
}
