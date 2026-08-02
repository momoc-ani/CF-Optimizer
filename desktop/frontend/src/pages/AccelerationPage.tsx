import { ActionIcon, Alert, Button, Group, SimpleGrid, Stack, Switch, Text, Textarea, TextInput, Tooltip } from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { zodResolver } from '@hookform/resolvers/zod';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { RefreshCw, Save, ScanSearch, ShieldAlert, ShieldCheck } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { Controller, useForm } from 'react-hook-form';
import { z } from 'zod';
import { request } from '../api/client';
import { queryKeys, useAccelerationDomains, useConfig, useRoutes, useStatus } from '../api/hooks';
import type { AppConfig, DomainDiscovery, DomainDiscoveryResult, RouteTransaction } from '../api/types';
import { DataTable } from '../components/DataTable';
import { ErrorState, LoadingState, Metric, PageHeader, Section } from '../components/Page';
import { StatusBadge } from '../components/StatusBadge';
import { joinConfigLines, parseDomainLines } from '../lib/configCollections';
import { findVerifiedDomainRoute, isDomainAccelerated } from '../lib/domainAcceleration';
import { formatDate } from '../lib/format';

const durationPattern = /^\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h)$/;
const accelerationSettingsSchema = z.object({
  enabled: z.boolean(),
  manualDomains: z.string(),
  excludedDomains: z.string(),
  autoDiscover: z.boolean(),
  autoApply: z.boolean(),
  discoveryInterval: z.string().regex(durationPattern, '请输入带单位的发现间隔，例如 15s'),
});

type AccelerationSettingsForm = z.infer<typeof accelerationSettingsSchema>;

const adapterLabels: Record<string, string> = {
  'generic-route': 'Generic Route',
  mihomo: 'Mihomo DIRECT',
  'sing-box': 'sing-box DIRECT',
  xray: 'Xray DIRECT',
  external: 'External RPC',
  'windows-hosts': 'Windows Hosts',
};

interface DomainRow extends DomainDiscovery {
  isAccelerated: boolean;
  isRouteVerified: boolean;
  policyLabel: string;
  verifiedRoute?: RouteTransaction;
}

/** describePolicy 将已验证适配器转换为不泄露配置内容的策略摘要。 */
function describePolicy(adapters: string[] = []): string {
  if (adapters.length === 0) return '待验证';
  const proxyAdapters = adapters.filter((adapter) => !['generic-route', 'windows-hosts'].includes(adapter));
  if (proxyAdapters.length > 0) return proxyAdapters.map((adapter) => adapterLabels[adapter] ?? adapter).join(' + ');
  return adapters.includes('generic-route') ? '系统直连' : '域名映射';
}

/** accelerationFormFromConfig 将域名加速配置投影为独立页面表单。 */
function accelerationFormFromConfig(config: AppConfig['acceleration']): AccelerationSettingsForm {
  return {
    enabled: config.enabled,
    manualDomains: joinConfigLines(config.manual_domains),
    excludedDomains: joinConfigLines(config.excluded_domains),
    autoDiscover: config.auto_discover,
    autoApply: config.auto_apply,
    discoveryInterval: config.discovery_interval,
  };
}

/** mergeAccelerationConfig 仅更新域名加速字段并保留完整后台配置。 */
function mergeAccelerationConfig(config: AppConfig, form: AccelerationSettingsForm): AppConfig {
  return {
    ...config,
    acceleration: {
      ...config.acceleration,
      enabled: form.enabled,
      manual_domains: parseDomainLines(form.manualDomains),
      excluded_domains: parseDomainLines(form.excludedDomains),
      auto_discover: form.autoDiscover,
      auto_apply: form.autoApply,
      discovery_interval: form.discoveryInterval,
    },
  };
}

/** AccelerationPage 展示域名映射及经物理出口验证的最终加速状态。 */
export function AccelerationPage() {
  const domains = useAccelerationDomains();
  const routes = useRoutes();
  const status = useStatus();
  const config = useConfig();
  const queryClient = useQueryClient();
  const [selectedDomain, setSelectedDomain] = useState<string>();
  const form = useForm<AccelerationSettingsForm>({
    resolver: zodResolver(accelerationSettingsSchema),
    defaultValues: {
      enabled: true,
      manualDomains: 'ani.momoc.top',
      excludedDomains: '',
      autoDiscover: true,
      autoApply: true,
      discoveryInterval: '15s',
    },
  });
  useEffect(() => {
    if (config.data) form.reset(accelerationFormFromConfig(config.data.acceleration));
  }, [config.data, form]);

  const rows = useMemo<DomainRow[]>(() => (domains.data?.domains ?? []).map((domain) => {
    const addresses = domain.accelerated_addresses ?? [];
    const verifiedRoutes = addresses.map((address) => findVerifiedDomainRoute(address, routes.data ?? [], status.data?.physical_path));
    return {
      ...domain,
      isAccelerated: isDomainAccelerated(domain, routes.data ?? [], status.data?.physical_path),
      isRouteVerified: addresses.length > 0 && verifiedRoutes.every(Boolean),
      policyLabel: describePolicy(domain.verified_adapters),
      verifiedRoute: verifiedRoutes.find(Boolean),
    };
  }), [domains.data?.domains, routes.data, status.data?.physical_path]);
  const selected = rows.find((row) => row.domain === selectedDomain) ?? rows[0];
  const acceleratedCount = rows.filter((row) => row.isAccelerated).length;
  const pendingCount = rows.length - acceleratedCount;
  const accelerationConfig = config.data?.acceleration;
  const isAutomatic = Boolean(accelerationConfig?.enabled && accelerationConfig.auto_discover && accelerationConfig.auto_apply);

  const columns = useMemo<ColumnDef<DomainRow>[]>(() => [
    { accessorKey: 'domain', header: '域名', size: 220, cell: ({ getValue }) => <Text ff="monospace" size="sm" fw={650}>{String(getValue())}</Text> },
    { accessorKey: 'source', header: '来源', size: 105, cell: ({ getValue }) => String(getValue()) === 'manual' ? '手动' : '自动发现' },
    { id: 'mapping', header: '优选 IP 映射', size: 220, accessorFn: (row) => row.accelerated_addresses?.join(', '), cell: ({ row }) => <Stack gap={2}>{(row.original.accelerated_addresses ?? []).map((address) => <Text key={address} ff="monospace" size="sm">{address}</Text>)}{!row.original.accelerated_addresses?.length && <Text c="dimmed" size="sm">待映射</Text>}</Stack> },
    { accessorKey: 'cloudflare_verified', header: 'Cloudflare', size: 118, cell: ({ getValue }) => <StatusBadge label={getValue<boolean>() ? '已确认' : '待确认'} tone={getValue<boolean>() ? 'verified' : 'neutral'} /> },
    { accessorKey: 'preflight_verified', header: 'SNI / Host', size: 115, cell: ({ getValue }) => <StatusBadge label={getValue<boolean>() ? '已验证' : '待验证'} tone={getValue<boolean>() ? 'verified' : 'neutral'} /> },
    { accessorKey: 'policyLabel', header: '直连策略', size: 180, cell: ({ row }) => <StatusBadge label={row.original.policyLabel} tone={row.original.active ? 'verified' : 'neutral'} /> },
    { accessorKey: 'isRouteVerified', header: '物理路由', size: 120, cell: ({ getValue }) => <StatusBadge label={getValue<boolean>() ? '已验证' : '待验证'} tone={getValue<boolean>() ? 'verified' : 'warning'} /> },
    { accessorKey: 'last_seen_at', header: '最近发现', size: 155, cell: ({ getValue }) => formatDate(String(getValue())) },
    { accessorKey: 'isAccelerated', header: '最终状态', size: 120, cell: ({ row }) => <StatusBadge label={row.original.isAccelerated ? '已加速' : row.original.last_error ? '验证失败' : '待验证'} tone={row.original.isAccelerated ? 'verified' : row.original.last_error ? 'failed' : 'pending'} /> },
  ], []);

  const discover = useMutation({
    mutationFn: () => request<DomainDiscoveryResult>('acceleration.discover'),
    onSuccess: async (result) => {
      notifications.show({ color: result.activated > 0 ? 'green' : 'blue', title: '发现完成', message: `观察 ${result.observed} 个连接，确认 ${result.verified} 个域名，激活 ${result.activated} 个` });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.accelerationDomains }),
        queryClient.invalidateQueries({ queryKey: queryKeys.routes }),
        queryClient.invalidateQueries({ queryKey: queryKeys.status }),
      ]);
    },
    onError: (error: Error) => notifications.show({ color: 'red', title: '发现失败', message: error.message }),
  });

  const savePolicy = useMutation({
    mutationFn: (next: AppConfig) => request<{ saved: boolean; restart_required: boolean }>('config.update', { config: next }),
    onSuccess: async (result) => {
      notifications.show({ color: 'green', title: '加速策略已保存', message: result.restart_required ? '后台服务重启后应用全部更改。' : '更改已经应用。' });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.config }),
        queryClient.invalidateQueries({ queryKey: queryKeys.accelerationDomains }),
      ]);
    },
    onError: (error: Error) => notifications.show({ color: 'red', title: '保存失败', message: error.message }),
  });

  const refreshAll = () => void Promise.all([domains.refetch(), routes.refetch(), status.refetch(), config.refetch()]);
  const isLoading = domains.isLoading || routes.isLoading || status.isLoading || config.isLoading;
  const readError = domains.error ?? routes.error ?? status.error ?? config.error;
  if (isLoading) return <LoadingState rows={8} />;
  if (readError) return <ErrorState message={readError.message} onRetry={refreshAll} />;
  if (!config.data) return <ErrorState message="后台未返回域名加速配置" onRetry={refreshAll} />;

  const currentIPv4 = status.data?.state.current_ipv4?.policy_verified ? status.data.state.current_ipv4.ip : '—';
  const currentIPv6 = status.data?.state.current_ipv6?.policy_verified ? status.data.state.current_ipv6.ip : '—';
  const physicalPath = status.data?.physical_path;
  const errors = form.formState.errors;
  const submitPolicy = form.handleSubmit((values) => savePolicy.mutate(mergeAccelerationConfig(config.data, values)));
  return (
    <Stack gap="lg">
      <PageHeader
        title="域名加速"
        description="Cloudflare 域名发现、优选 IP 映射与直连验证"
        actions={<>
          <Tooltip label="刷新域名、路由与状态"><ActionIcon aria-label="刷新域名加速状态" variant="light" loading={domains.isFetching || routes.isFetching || status.isFetching} onClick={refreshAll}><RefreshCw size={17} /></ActionIcon></Tooltip>
          <Button leftSection={<ScanSearch size={16} />} loading={discover.isPending} disabled={!accelerationConfig?.enabled || !accelerationConfig.auto_discover} onClick={() => discover.mutate()}>立即发现</Button>
        </>}
      />

      <Alert color={isAutomatic ? 'green' : 'yellow'} icon={isAutomatic ? <ShieldCheck size={18} /> : <ShieldAlert size={18} />} title={isAutomatic ? '自动加速已开启' : '自动加速未完全开启'}>
        <Text size="sm">域名加速 {accelerationConfig?.enabled ? '开启' : '关闭'} · 自动发现 {accelerationConfig?.auto_discover ? '开启' : '关闭'} · 自动应用 {accelerationConfig?.auto_apply ? '开启' : '关闭'} · 周期 {accelerationConfig?.discovery_interval ?? '—'}</Text>
      </Alert>

      <form onSubmit={submitPolicy}>
        <Section
          title="加速策略"
          aside={<Button type="submit" size="compact-sm" leftSection={<Save size={15} />} loading={savePolicy.isPending} disabled={!form.formState.isDirty}>保存策略</Button>}
        >
          <Stack gap="md">
            <SimpleGrid cols={{ base: 1, sm: 2, lg: 4 }} spacing="md">
              <Controller control={form.control} name="enabled" render={({ field }) => <Switch label="启用 Cloudflare 域名加速" checked={field.value} onChange={field.onChange} />} />
              <Controller control={form.control} name="autoDiscover" render={({ field }) => <Switch label="自动发现 Cloudflare 域名" checked={field.value} onChange={field.onChange} />} />
              <Controller control={form.control} name="autoApply" render={({ field }) => <Switch color="orange" label="自动应用已验证域名" checked={field.value} onChange={field.onChange} />} />
              <Controller control={form.control} name="discoveryInterval" render={({ field }) => <TextInput {...field} label="发现间隔" error={errors.discoveryInterval?.message} />} />
            </SimpleGrid>
            <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="md">
              <Controller control={form.control} name="manualDomains" render={({ field }) => <Textarea {...field} label="手动域名" autosize minRows={3} ff="monospace" placeholder="每行一个精确域名" />} />
              <Controller control={form.control} name="excludedDomains" render={({ field }) => <Textarea {...field} label="排除域名" autosize minRows={3} ff="monospace" placeholder="每行一个精确域名" />} />
            </SimpleGrid>
          </Stack>
        </Section>
      </form>

      <SimpleGrid cols={{ base: 2, md: 4 }} spacing="sm">
        <Metric label="优选 IPv4" value={<Text ff="monospace" inherit>{currentIPv4}</Text>} detail="策略已验证的当前节点" accent="#1677a6" />
        <Metric label="优选 IPv6" value={<Text ff="monospace" inherit>{currentIPv6}</Text>} detail="未验证时不参与映射" accent="#7950f2" />
        <Metric label="已加速域名" value={acceleratedCount} detail={`${pendingCount} 个仍待完整验证`} accent="#2b8a5a" />
        <Metric label="物理出口" value={physicalPath?.interface ?? '未发现'} detail={physicalPath?.gateway_ipv4 ?? physicalPath?.gateway_ipv6 ?? '缺少网关证据'} accent="#c47a12" />
      </SimpleGrid>

      <div className="split-layout acceleration-layout">
        <Section title="域名与验证状态" aside={<Text size="xs" c="dimmed">共 {rows.length} 个域名</Text>}>
          <DataTable columns={columns} data={rows} minWidth={1350} rowKey={(row) => row.domain} onRowClick={(row) => setSelectedDomain(row.domain)} emptyTitle="尚无加速域名" emptyDetail="后台尚未返回手动域名或已验证的自动发现记录。" />
        </Section>
        <Section title="验证证据" className="inspector-section">
          {selected ? (
            <Stack gap="md">
              <Group justify="space-between" wrap="nowrap"><Text ff="monospace" fw={650}>{selected.domain}</Text><StatusBadge label={selected.isAccelerated ? '已加速' : selected.last_error ? '验证失败' : '待验证'} tone={selected.isAccelerated ? 'verified' : selected.last_error ? 'failed' : 'pending'} /></Group>
              <div className="property-grid">
                <Text c="dimmed">来源</Text><Text>{selected.source === 'manual' ? '手动域名' : 'Mihomo 自动发现'}</Text>
                <Text c="dimmed">物理 DNS</Text><Text ff="monospace">{selected.last_resolved_addresses?.join(', ') || '—'}</Text>
                <Text c="dimmed">优选映射</Text><Text ff="monospace">{selected.accelerated_addresses?.join(', ') || '—'}</Text>
                <Text c="dimmed">TLS SNI</Text><Text ff="monospace">{selected.domain}</Text>
                <Text c="dimmed">HTTP Host</Text><Text ff="monospace">{selected.domain}</Text>
                <Text c="dimmed">已验证策略</Text><Text>{selected.verified_adapters?.map((adapter) => adapterLabels[adapter] ?? adapter).join(', ') || '—'}</Text>
                <Text c="dimmed">物理接口</Text><Text>{selected.verifiedRoute?.verification?.interface ?? '—'}</Text>
                <Text c="dimmed">源地址</Text><Text ff="monospace">{selected.verifiedRoute?.verification?.source_address ?? '—'}</Text>
                <Text c="dimmed">网关</Text><Text ff="monospace">{selected.verifiedRoute?.verification?.gateway ?? '—'}</Text>
                <Text c="dimmed">路由事务</Text><Text ff="monospace">{selected.verifiedRoute?.id ?? '—'}</Text>
                <Text c="dimmed">策略应用</Text><Text>{formatDate(selected.applied_at)}</Text>
                <Text c="dimmed">最近发现</Text><Text>{formatDate(selected.last_seen_at)}</Text>
                <Text c="dimmed">错误</Text><Text c={selected.last_error ? 'red' : undefined}>{selected.last_error ?? '无'}</Text>
              </div>
              <Alert color={selected.isAccelerated ? 'green' : 'yellow'} icon={selected.isAccelerated ? <ShieldCheck size={17} /> : <ShieldAlert size={17} />} title={selected.isAccelerated ? 'HTTPS 与物理直连证据完整' : '尚不能确认域名已加速'}>
                {selected.isAccelerated ? '系统映射连接的远端地址属于优选 IP，直连策略、物理接口、源地址和网关均已验证。' : '缺失的验证项会保留为待验证或失败，不会显示已加速。'}
              </Alert>
            </Stack>
          ) : <Text c="dimmed">选择一个域名查看验证证据。</Text>}
        </Section>
      </div>
    </Stack>
  );
}
