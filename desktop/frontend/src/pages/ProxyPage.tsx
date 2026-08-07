import { Alert, Button, Drawer, Group, SimpleGrid, Stack, Text } from '@mantine/core';
import type { ColumnDef } from '@tanstack/react-table';
import { Cable, RefreshCw, ShieldCheck, ShieldQuestion } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useProxies, useStatus } from '../api/hooks';
import type { PolicyGuardStatus, ProxyDetection } from '../api/types';
import { DataTable } from '../components/DataTable';
import { ErrorState, LoadingState, Metric, PageHeader, Section } from '../components/Page';
import { StatusBadge } from '../components/StatusBadge';

interface AdapterRow extends ProxyDetection {
  id: string;
  label: string;
  mode: string;
  verification: string;
  guard?: PolicyGuardStatus;
}

const adapterMetadata: Record<string, Omit<AdapterRow, keyof ProxyDetection | 'id' | 'guard'>> = {
  'generic-route': { label: 'Generic Route', mode: 'Host route', verification: '路由查询与源地址' },
  mihomo: { label: 'Mihomo / Clash', mode: 'Provider + API', verification: 'Controller rules' },
  'sing-box': { label: 'sing-box', mode: 'Managed fragment', verification: '配置校验与重载' },
  xray: { label: 'Xray / V2Ray', mode: 'Managed fragment', verification: '配置校验与重载' },
  external: { label: 'External RPC', mode: 'JSON-RPC v1', verification: '扩展进程回执' },
};

/** buildAdapterRows 将后台检测结果补充为稳定的界面展示模型。 */
function buildAdapterRows(detections: Record<string, ProxyDetection>, guards: Record<string, PolicyGuardStatus>): AdapterRow[] {
  return Object.entries(detections).map(([id, detection]) => ({
    id,
    label: adapterMetadata[id]?.label ?? id,
    mode: adapterMetadata[id]?.mode ?? 'Backend adapter',
    verification: adapterMetadata[id]?.verification ?? '后台适配器验证',
    guard: guards[id],
    ...detection,
  }));
}

/** ProxyPage 展示代理内核检测结果和可审计的验证边界。 */
export function ProxyPage() {
  const proxies = useProxies();
  const status = useStatus();
  const rows = useMemo(() => buildAdapterRows(proxies.data ?? {}, status.data?.policy_guards ?? {}), [proxies.data, status.data?.policy_guards]);
  const [selectedID, setSelectedID] = useState<string>();
  const selected = rows.find((row) => row.id === selectedID);
  const columns = useMemo<ColumnDef<AdapterRow>[]>(() => [
    { accessorKey: 'label', header: '适配器', size: 180, cell: ({ row }) => <div><Text fw={650}>{row.original.label}</Text><Text size="xs" c="dimmed">{row.original.version ?? '版本未知'}</Text></div> },
    { accessorKey: 'manageable', header: '管理', size: 100, cell: ({ row }) => <StatusBadge label={row.original.manageable ? '可管理' : row.original.present ? '只读' : '不可用'} tone={row.original.manageable ? 'verified' : row.original.present ? 'warning' : 'neutral'} /> },
    { id: 'guard', header: '规则守护', size: 120, cell: ({ row }) => row.original.guard ? <StatusBadge label={guardLabel(row.original.guard)} tone={guardTone(row.original.guard)} /> : <StatusBadge label="未启用" tone="neutral" /> },
    { accessorKey: 'endpoint', header: '控制端', size: 190, cell: ({ getValue }) => <Text size="sm" ff="monospace">{String(getValue() ?? '—')}</Text> },
    { accessorKey: 'mode', header: '应用方式', size: 170 },
    { accessorKey: 'verification', header: '验证依据', size: 180 },
    { accessorKey: 'message', header: '后台结果', size: 300, cell: ({ getValue }) => <Text size="sm" c="dimmed">{String(getValue() ?? '无附加信息')}</Text> },
  ], []);
  const openAdapterDetails = (row: AdapterRow) => setSelectedID(row.id);
  const closeAdapterDetails = () => setSelectedID(undefined);
  if (proxies.isLoading) return <LoadingState rows={7} />;
  if (proxies.isError) return <ErrorState message={proxies.error.message} onRetry={() => proxies.refetch()} />;
  const detected = rows.filter((row) => row.present).length;
  return (
    <Stack gap="lg">
      <PageHeader
        title="代理适配"
        description="代理内核检测、应用方式与后台验证结果"
        actions={<Button variant="light" leftSection={<RefreshCw size={16} />} loading={proxies.isFetching} onClick={() => proxies.refetch()}>重新检测</Button>}
      />
      <SimpleGrid cols={{ base: 2, md: 4 }} spacing="sm">
        <Metric label="已注册适配器" value={rows.length} detail="由后台注册表提供" accent="#1677a6" />
        <Metric label="已检测" value={detected} detail="当前环境可访问" accent="#2b8a5a" />
        <Metric label="未检测" value={rows.length - detected} detail="未配置或不可访问" />
        <Metric label="规则守护" value={status.data?.policy_guards?.mihomo ? guardLabel(status.data.policy_guards.mihomo) : '未启用'} detail="仅恢复上次成功策略" accent="#7950f2" />
      </SimpleGrid>

      {detected === 0 && <Alert color="yellow" icon={<ShieldQuestion size={18} />} title="未发现可用代理适配器">测速仍可运行；只有后台验证成功的策略才会标记为已验证。</Alert>}

      <Section title="适配器状态" aside={<Text size="xs" c="dimmed">点击行打开右侧验证详情 · 共 {rows.length} 个适配器</Text>}>
        <DataTable columns={columns} data={rows} minWidth={1240} rowKey={(row) => row.id} onRowClick={openAdapterDetails} emptyTitle="没有适配器" emptyDetail="后台未返回已注册的代理适配器。" />
      </Section>

      <Drawer opened={Boolean(selected)} onClose={closeAdapterDetails} position="right" size={520} title="验证详情">
        {selected && (
          <Stack gap="md">
            <Group justify="space-between" wrap="nowrap">
              <Group gap="xs"><Cable size={18} /><Text fw={650}>{selected.label}</Text></Group>
              <StatusBadge label={selected.present ? '已检测' : '未检测'} tone={selected.present ? 'verified' : 'neutral'} />
            </Group>
            <div className="property-grid">
              <Text c="dimmed">版本</Text><Text ff="monospace">{selected.version ?? '—'}</Text>
              <Text c="dimmed">控制端</Text><Text ff="monospace">{selected.endpoint ?? '—'}</Text>
              <Text c="dimmed">活动配置</Text><Text ff="monospace">{selected.config_path ?? '—'}</Text>
              <Text c="dimmed">应用方式</Text><Text>{selected.mode}</Text>
              <Text c="dimmed">验证依据</Text><Text>{selected.verification}</Text>
              <Text c="dimmed">后台信息</Text><Text>{selected.message ?? '—'}</Text>
              <Text c="dimmed">守护状态</Text><Text>{selected.guard ? guardLabel(selected.guard) : '未启用'}</Text>
              <Text c="dimmed">代理活动</Text><Text>{selected.guard ? activityLabel(selected.guard) : '—'}</Text>
              <Text c="dimmed">最近验证</Text><Text>{selected.guard?.last_verified_at ? new Date(selected.guard.last_verified_at).toLocaleString() : '—'}</Text>
              <Text c="dimmed">守护结果</Text><Text>{selected.guard?.message ?? '—'}</Text>
            </div>
            <Alert color={selected.present ? 'green' : 'gray'} icon={selected.present ? <ShieldCheck size={17} /> : <ShieldQuestion size={17} />} title={selected.present ? '检测已完成' : '没有验证证据'}>
              {selected.present ? '此状态仅证明后台检测接口可用；策略是否生效以实际应用后的验证事务为准。' : '当前不会声称该代理的 DIRECT 策略已经生效。'}
            </Alert>
          </Stack>
        )}
      </Drawer>
    </Stack>
  );
}

function guardLabel(guard: PolicyGuardStatus) {
  return ({ offline: '内核离线', no_policy: '无历史策略', standby: '待机', checking: '检查中', drifted: '规则漂移', restoring: '恢复中', verified: '已验证', retry_wait: '等待重试', failed: '恢复失败' } as const)[guard.state];
}

function guardTone(guard: PolicyGuardStatus): 'verified' | 'pending' | 'warning' | 'failed' | 'neutral' {
  if (guard.state === 'verified') return 'verified';
  if (guard.state === 'checking' || guard.state === 'restoring') return 'pending';
  if (guard.state === 'drifted' || guard.state === 'retry_wait') return 'warning';
  if (guard.state === 'failed') return 'failed';
  return 'neutral';
}

function activityLabel(guard: PolicyGuardStatus) {
  if (guard.tun_active) return 'TUN 已启用';
  if (guard.system_proxy_active) return '系统代理已启用';
  if (guard.activity === 'unknown') return '状态未知';
  return guard.online ? '代理未启用' : '内核离线';
}
