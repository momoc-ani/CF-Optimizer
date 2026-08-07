import { Alert, Button, Group, ScrollArea, SimpleGrid, Stack, Table, Text, ThemeIcon } from '@mantine/core';
import { useMutation } from '@tanstack/react-query';
import { useMemo, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { Activity, Cable, Clock3, Network, Play, Route, ShieldCheck } from 'lucide-react';
import { request } from '../api/client';
import { useConfig, useHistory, useLatestBenchmark, useProxies, useRanges, useStatus } from '../api/hooks';
import type { BenchmarkResult, QuickStartMode, QuickStartPlan, Selection } from '../api/types';
import { formatDate, formatDuration, formatMbps, formatScore } from '../lib/format';
import { EmptyState, LoadingState, Metric, PageHeader, Section } from '../components/Page';
import { StatusBadge } from '../components/StatusBadge';
import { useRun } from '../hooks/useRun';
import { QuickStartDialog } from '../components/QuickStartDialog';
import { useUIStore } from '../state/ui';
import { countCurrentVerifiedRoutes } from '../lib/routeSummary';
import { selectTopBenchmarkResults } from '../lib/benchmarkSnapshot';
import { describeQuickStartResult, formatRunEventDetail, formatRunEventTitle, presentLatestIPv4Decision, presentSchedule } from '../lib/overview';

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

/** BenchmarkVerificationBadge 将测速阶段状态压缩成总览可扫描的验证结论。 */
function BenchmarkVerificationBadge({ result }: { result: BenchmarkResult }) {
  if (result.download_verified) return <StatusBadge label="下载已验证" tone="verified" />;
  if (result.tls_verified) return <StatusBadge label="TLS 已验证" tone="verified" />;
  if (result.qualified) return <StatusBadge label="TCP 合格" tone="pending" />;
  return <StatusBadge label="未通过" tone="neutral" />;
}

/** LatestBenchmarkTable 展示最近成功保存结果的前 download_top 名。 */
function LatestBenchmarkTable({ results }: { results: BenchmarkResult[] }) {
  return (
    <ScrollArea type="auto" className="table-scroll">
      <Table striped highlightOnHover withRowBorders verticalSpacing="xs" miw={720} className="data-table">
        <Table.Thead><Table.Tr><Table.Th>#</Table.Th><Table.Th>IP</Table.Th><Table.Th>平均延迟</Table.Th><Table.Th>吞吐</Table.Th><Table.Th>评分</Table.Th><Table.Th>验证</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>{results.map((result, index) => <Table.Tr key={result.ip}><Table.Td className="tabular">{index + 1}</Table.Td><Table.Td><Text ff="monospace" size="sm">{result.ip}</Text></Table.Td><Table.Td className="tabular">{formatDuration(result.avg_latency)}</Table.Td><Table.Td className="tabular">{formatMbps(result.mbps)}</Table.Td><Table.Td><Text fw={650} className="tabular">{formatScore(result.score)}</Text></Table.Td><Table.Td><BenchmarkVerificationBadge result={result} /></Table.Td></Table.Tr>)}</Table.Tbody>
      </Table>
    </ScrollArea>
  );
}

export function OverviewPage() {
  const status = useStatus();
  const config = useConfig();
  const proxies = useProxies();
  const ranges = useRanges();
  const history = useHistory();
  const latestBenchmark = useLatestBenchmark(status.data?.state.last_ended_at);
  const run = useRun();
  const setPage = useUIStore((current) => current.setPage);
  const [isConfirming, setIsConfirming] = useState(false);
  const [quickStartPlan, setQuickStartPlan] = useState<QuickStartPlan>();
  const [quickStartMode, setQuickStartMode] = useState<QuickStartMode>('apply_once');
  const [didRunBenchmarkOnly, setDidRunBenchmarkOnly] = useState(false);
  const planMutation = useMutation({ mutationFn: () => request<QuickStartPlan>('quickstart.plan') });

  /** prepareQuickStart 请求只读计划，并在已有持续授权时复用确认。 */
  const prepareQuickStart = async (reuseConfirmedMaintenance = true) => {
    setDidRunBenchmarkOnly(false);
    let plan: QuickStartPlan;
    try {
      plan = await planMutation.mutateAsync();
    } catch {
      return;
    }
    setQuickStartPlan(plan);
    if (reuseConfirmedMaintenance && plan.can_apply && plan.auto_maintenance_enabled) {
      try {
        await run.runQuickStart({ plan_id: plan.plan_id, mode: 'apply_and_remember', force_range_refresh: false });
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        if (message.includes('plan_expired') || message.includes('plan_stale') || message.includes('plan_not_found')) await prepareQuickStart(false);
      }
      return;
    }
    setIsConfirming(true);
  };
  /** runConfirmedPlan 执行当前计划；计划失效时自动回到新的确认框。 */
  const runConfirmedPlan = async () => {
    if (!quickStartPlan) return;
    setIsConfirming(false);
    try {
      await run.runQuickStart({ plan_id: quickStartPlan.plan_id, mode: quickStartMode, force_range_refresh: false });
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      if (message.includes('plan_expired') || message.includes('plan_stale') || message.includes('plan_not_found')) {
        await prepareQuickStart(false);
      }
    }
  };
  /** runBenchmarkOnly 在自动发现失败时保持完全只读的降级路径。 */
  const runBenchmarkOnly = async () => {
    setIsConfirming(false);
    setDidRunBenchmarkOnly(true);
    try {
      await run.run({ force_range_refresh: false, apply_policy: false });
    } catch {
      // RunContext 已将可恢复错误映射到全局任务状态。
    }
  };
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
  const verifiedRoutes = countCurrentVerifiedRoutes(state);
  const schedule = presentSchedule(status.data?.schedule, Boolean(state?.running), formatDate);
  const latestIPv4Decision = presentLatestIPv4Decision(history.data?.[0]);
  const downloadTop = config.data?.benchmark.download_top ?? 20;
  const latestResults = selectTopBenchmarkResults(latestBenchmark.data?.results, downloadTop);
  return (
    <Stack gap="lg">
      <PageHeader title="总览" description="后台服务、当前节点与已验证直连策略" actions={<Button leftSection={<Play size={16} />} loading={planMutation.isPending || run.running} onClick={() => void prepareQuickStart()}>一键优选</Button>} />
      {planMutation.isError && <Alert color="red" title="预检失败" withCloseButton onClose={() => planMutation.reset()}>{planMutation.error.message}</Alert>}
      {run.quickStartResult && <Alert color={run.quickStartResult.status === 'verified' ? 'green' : run.quickStartResult.status === 'rolled_back' ? 'gray' : 'yellow'} title={run.quickStartResult.status === 'verified' ? '已验证' : run.quickStartResult.status === 'rolled_back' ? '已回滚' : '部分完成'}>{describeQuickStartResult(run.quickStartResult)}</Alert>}
      {didRunBenchmarkOnly && run.report && !run.running && !run.error && <Alert color="blue" title="仅测速完成">候选结果已生成，没有修改系统路由或代理策略。</Alert>}
      {run.error && !run.running && <Alert color={run.error.message.includes('cancelled') ? 'gray' : 'red'} title={run.error.message.includes('cancelled') ? '任务已取消' : '任务未完成'}>{run.error.message}</Alert>}
      {state?.last_error && <Alert color="red" title="上次任务失败">{state.last_error}</Alert>}
      <SimpleGrid cols={{ base: 2, md: 4 }} spacing="sm">
        <Metric label="后台服务" value={<Group gap={7}><ThemeIcon size="sm" color={state ? 'green' : 'red'} variant="light"><Activity size={14} /></ThemeIcon>{state?.running ? '优选运行中' : '运行正常'}</Group>} detail={`协议 v${status.data?.protocol_version ?? '—'}`} accent="#2b8a5a" />
        <Metric label="下次节点池刷新" value={schedule.value} detail={schedule.detail} accent="#1677a6" />
        <Metric label="代理适配" value={`${detected} 个已检测`} detail="仅显示可验证的适配器状态" accent="#7a59a8" />
        <Metric label="Cloudflare 网段" value={`${(ranges.data?.ipv4.length ?? 0) + (ranges.data?.ipv6.length ?? 0)} 条`} detail={`${ranges.data?.source ?? '读取中'} · ${formatDate(ranges.data?.fetched_at)}`} accent="#c47a12" />
        <Metric label="测速节点池" value={state?.node_pool ? (state.node_pool.stale ? '已过期，尝试刷新' : '有效，可复用') : '尚未建立'} detail={state?.node_pool ? `${state.node_pool.candidates} 个候选 · 有效至 ${formatDate(state.node_pool.valid_until)}` : '首次一键优选后生成'} accent="#2b8a5a" />
      </SimpleGrid>

      <Section title="当前生效节点" aside={<StatusBadge label={verifiedRoutes > 0 ? `${verifiedRoutes} 条路由已验证` : '无已验证路由'} tone={verifiedRoutes > 0 ? 'verified' : 'warning'} />}>
        <Stack gap={0} className="selection-list">
          <SelectionRow title="IPv4" selection={state?.current_ipv4} interfaceName={path?.interface} />
          <SelectionRow title="IPv6" selection={state?.current_ipv6} interfaceName={path?.interface} />
        </Stack>
        {latestIPv4Decision && (
          <SimpleGrid cols={{ base: 1, sm: 3 }} spacing="sm" className="selection-summary">
            <div><Text size="xs" c="dimmed">最近测速第一名</Text><Text ff="monospace" fw={650}>{latestIPv4Decision.best.ip}</Text></div>
            <div><Text size="xs" c="dimmed">本轮选定节点</Text><Text ff="monospace" fw={650}>{latestIPv4Decision.selectedIP}</Text></div>
            <div><Text size="xs" c="dimmed">切换决策</Text><Text fw={650}>{latestIPv4Decision.decision}</Text></div>
          </SimpleGrid>
        )}
      </Section>

      <Section title="上次优选测速结果" aside={latestBenchmark.data?.run_id && <Text size="xs" c="dimmed">前 {downloadTop} 名 · {formatDate(latestBenchmark.data.finished_at)}</Text>}>
        {latestBenchmark.isLoading ? <LoadingState rows={3} /> : latestBenchmark.isError ? <Alert color="yellow" title="最近结果读取失败">{latestBenchmark.error.message}</Alert> : latestResults.length ? <LatestBenchmarkTable results={latestResults} /> : <EmptyState title="暂无已保存测速结果" detail="完成一次成功优选后，这里会保留最近一轮的前 N 名结果。" />}
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
        {(history.data ?? []).slice(0, 4).map((item) => <div className="event-row" key={item.id}><span className={`event-severity ${item.error ? 'warning' : 'success'}`} /><Text size="sm" className="event-time tabular">{formatDate(item.finished_at)}</Text><Text size="sm" fw={550}>{formatRunEventTitle(item)}</Text><Text size="sm" c="dimmed" lineClamp={1}>{formatRunEventDetail(item)}</Text></div>)}
      </Section>
      <QuickStartDialog
        opened={isConfirming}
        plan={quickStartPlan}
        mode={quickStartMode}
        running={run.running}
        onModeChange={setQuickStartMode}
        onClose={() => setIsConfirming(false)}
        onConfirm={runConfirmedPlan}
        onBenchmarkOnly={runBenchmarkOnly}
        onAdvanced={() => { setIsConfirming(false); setPage('settings'); }}
      />
    </Stack>
  );
}
