import { Alert, Button, Group, SimpleGrid, Stack, Text } from '@mantine/core';
import { useMutation } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { Network, RefreshCw, Route, ScanSearch, ShieldAlert, ShieldCheck } from 'lucide-react';
import { useMemo, useState } from 'react';
import { request } from '../api/client';
import { useRoutes, useStatus } from '../api/hooks';
import type { DiagnosticReport, RouteTransaction } from '../api/types';
import { DataTable } from '../components/DataTable';
import { ErrorState, LoadingState, Metric, PageHeader, Section } from '../components/Page';
import { StatusBadge } from '../components/StatusBadge';
import { formatDate } from '../lib/format';
import { countCurrentVerifiedRoutes, countTemporaryRoutesRequiringCleanup } from '../lib/routeSummary';
import { useUIStore } from '../state/ui';

/** routeTone 将事务状态转换为只表达已有证据的视觉状态。 */
function routeTone(state: string): 'verified' | 'pending' | 'failed' | 'rolled-back' | 'neutral' {
  if (state === 'verified') return 'verified';
  if (state === 'failed') return 'failed';
  if (state === 'rolled_back') return 'rolled-back';
  if (state === 'planned' || state === 'applied') return 'pending';
  return 'neutral';
}

/** routeTarget 从主机路由前缀提取诊断目标地址。 */
function routeTarget(transaction?: RouteTransaction): string {
  return transaction?.route.prefix.split('/')[0] ?? '';
}

/** RoutesPage 展示路由事务、物理路径和按需直连诊断证据。 */
export function RoutesPage() {
  const routes = useRoutes();
  const status = useStatus();
  const [selectedID, setSelectedID] = useState<string>();
  const setPage = useUIStore((current) => current.setPage);
  const rows = routes.data ?? [];
  const selected = rows.find((item) => item.id === selectedID) ?? rows[0];
  const diagnostic = useMutation({ mutationFn: (target: string) => request<DiagnosticReport>('diagnostics.route', { target }) });
  const columns = useMemo<ColumnDef<RouteTransaction>[]>(() => [
    { accessorKey: 'route.prefix', header: '目标前缀', size: 220, cell: ({ getValue }) => <Text ff="monospace" size="sm">{String(getValue())}</Text> },
    { accessorKey: 'route.gateway', header: '网关', size: 155, cell: ({ getValue }) => <Text ff="monospace" size="sm">{String(getValue())}</Text> },
    { accessorKey: 'route.interface', header: '接口', size: 130 },
    { accessorKey: 'route.metric', header: 'Metric', size: 76 },
    { accessorKey: 'temporary', header: '类型', size: 100, cell: ({ getValue }) => getValue<boolean>() ? '临时' : '持久主机路由' },
    { accessorKey: 'state', header: '事务状态', size: 112, cell: ({ getValue }) => <StatusBadge label={String(getValue())} tone={routeTone(String(getValue()))} /> },
    { accessorKey: 'updated_at', header: '更新时间', size: 150, cell: ({ getValue }) => formatDate(String(getValue())) },
  ], []);

  if (routes.isLoading || status.isLoading) return <LoadingState rows={7} />;
  if (routes.isError) return <ErrorState message={routes.error.message} onRetry={() => routes.refetch()} />;
  const historicalVerified = rows.filter((item) => item.state === 'verified').length;
  const currentVerified = countCurrentVerifiedRoutes(status.data?.state);
  const temporary = countTemporaryRoutesRequiringCleanup(rows);
  const path = status.data?.physical_path;
  const report = diagnostic.data;
  return (
    <Stack gap="lg">
      <PageHeader title="网络路由" description="物理出口、路由事务与直连诊断证据" actions={<Button variant="light" leftSection={<RefreshCw size={16} />} loading={routes.isFetching} onClick={() => Promise.all([routes.refetch(), status.refetch()])}>刷新状态</Button>} />
      {!path?.interface && <Alert color="yellow" icon={<ShieldAlert size={18} />} title="未发现可信物理出口">自动预检无法确定接口或网关。<Button ml="sm" size="compact-xs" variant="light" color="yellow" onClick={() => setPage('settings')}>打开高级设置</Button></Alert>}
      <SimpleGrid cols={{ base: 2, md: 4 }} spacing="sm">
        <Metric label="物理接口" value={path?.interface ?? '未发现'} detail={path?.interface_index ? `Index ${path.interface_index}` : '没有接口索引'} accent="#1677a6" />
        <Metric label="IPv4 网关" value={<Text ff="monospace" inherit>{path?.gateway_ipv4 ?? '—'}</Text>} detail={path?.source_ipv4?.[0] ?? '无源地址'} />
        <Metric label="当前已验证路由" value={currentVerified} detail={`${historicalVerified} 条历史验证记录`} accent="#2b8a5a" />
        <Metric label="活动临时路由" value={temporary} detail={temporary ? '任务结束后应清理' : '无候选网段残留'} accent={temporary ? '#c47a12' : '#75808a'} />
      </SimpleGrid>

      <div className="split-layout routes-layout">
        <Section title="路由事务" aside={<StatusBadge label={historicalVerified ? `${historicalVerified} 条历史验证记录` : '无验证记录'} tone={historicalVerified ? 'verified' : 'warning'} />}>
          <DataTable columns={columns} data={rows} minWidth={980} rowKey={(row) => row.id} onRowClick={(row) => { setSelectedID(row.id); diagnostic.reset(); }} emptyTitle="暂无路由事务" emptyDetail="系统路由管理关闭或尚未运行应用策略的优选任务。" />
        </Section>
        <Section title="路由诊断" className="inspector-section">
          {selected ? (
            <Stack gap="md">
              <Group justify="space-between" wrap="nowrap">
                <Group gap="xs"><Route size={18} /><Text ff="monospace" fw={650}>{selected.route.prefix}</Text></Group>
                <StatusBadge label={selected.state} tone={routeTone(selected.state)} />
              </Group>
              <div className="property-grid">
                <Text c="dimmed">事务 ID</Text><Text ff="monospace">{selected.id}</Text>
                <Text c="dimmed">操作</Text><Text>{selected.operation}</Text>
                <Text c="dimmed">预期接口</Text><Text>{selected.route.interface}</Text>
                <Text c="dimmed">验证源地址</Text><Text ff="monospace">{selected.verification?.source_address ?? '—'}</Text>
              </div>
              <Button leftSection={<ScanSearch size={16} />} loading={diagnostic.isPending} onClick={() => diagnostic.mutate(routeTarget(selected))}>运行直连诊断</Button>
              {diagnostic.isError && <Alert color="red" title="诊断失败">{diagnostic.error.message}</Alert>}
              {report && (
                <Stack gap="sm">
                  <Alert color={report.verified ? 'green' : 'yellow'} icon={report.verified ? <ShieldCheck size={17} /> : <ShieldAlert size={17} />} title={report.verified ? '路由与连接证据已验证' : '未能验证直连路径'}>
                    {report.verified ? `本地地址 ${report.direct_connection?.local_address ?? '未知'}；远端 ${report.direct_connection?.remote_address ?? report.target}` : report.direct_connection?.error ?? '后台未返回完整证据。'}
                  </Alert>
                  {(report.warnings ?? []).map((warning) => <Text key={warning} size="sm" c="orange">{warning}</Text>)}
                </Stack>
              )}
            </Stack>
          ) : <div className="empty-inspector"><Network size={24} /><Text c="dimmed">选择一条事务后运行诊断。</Text></div>}
        </Section>
      </div>
    </Stack>
  );
}
