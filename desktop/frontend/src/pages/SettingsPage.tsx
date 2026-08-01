import { Alert, Button, Checkbox, Group, NumberInput, SegmentedControl, SimpleGrid, Stack, Switch, Text, TextInput, Textarea, useMantineColorScheme } from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { zodResolver } from '@hookform/resolvers/zod';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Save, ShieldAlert } from 'lucide-react';
import { Controller, useForm } from 'react-hook-form';
import { useEffect } from 'react';
import { z } from 'zod';
import { request } from '../api/client';
import { queryKeys, useConfig } from '../api/hooks';
import type { AppConfig } from '../api/types';
import { ErrorState, LoadingState, PageHeader, Section } from '../components/Page';
import { joinConfigLines } from '../lib/configCollections';

const durationPattern = /^\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h)$/;
const settingsSchema = z.object({
  scheduleEnabled: z.boolean(),
  scheduleInterval: z.string().regex(durationPattern, '请输入带单位的时长，例如 6h 或 30m'),
  runOnNetworkChange: z.boolean(),
  ipv4: z.boolean(),
  ipv6: z.boolean(),
  candidates: z.number().int().min(1).max(100000),
  concurrency: z.number().int().min(1).max(2000),
  connectAttempts: z.number().int().min(1).max(20),
  lossLimitPercent: z.number().min(0).max(100),
  switchImprovementPercent: z.number().min(0).max(500),
  downloadURL: z.string().refine((value) => value === '' || /^https:\/\//i.test(value), '测速地址必须为空或使用 HTTPS'),
  tlsServerName: z.string(),
  rangeRefreshInterval: z.string().regex(durationPattern, '请输入带单位的时长，例如 24h'),
  rangeInclude: z.string(),
  rangeExclude: z.string(),
  proxyAutoDetect: z.boolean(),
  accelerationEnabled: z.boolean(),
  accelerationManualDomains: z.string(),
  accelerationExcludedDomains: z.string(),
  accelerationAutoDiscover: z.boolean(),
  accelerationAutoApply: z.boolean(),
  accelerationDiscoveryInterval: z.string().regex(durationPattern, '请输入带单位的发现间隔，例如 15s'),
  networkInterface: z.string(),
  gatewayIPv4: z.string(),
  gatewayIPv6: z.string(),
  manageRoutes: z.boolean(),
}).superRefine((value, context) => {
  if (value.manageRoutes && !value.networkInterface.trim()) context.addIssue({ code: 'custom', path: ['networkInterface'], message: '启用路由管理时必须填写物理接口' });
  if (value.manageRoutes && !value.gatewayIPv4.trim() && !value.gatewayIPv6.trim()) context.addIssue({ code: 'custom', path: ['gatewayIPv4'], message: '至少填写一个物理网关' });
});

type SettingsForm = z.infer<typeof settingsSchema>;

/** parseCIDRList 将多行或逗号分隔输入规范化为后台继续校验的字符串数组。 */
function parseCIDRList(value: string): string[] {
  return [...new Set(value.split(/[\n,]/).map((item) => item.trim()).filter(Boolean))];
}

/** parseDomainList 将多行精确域名规范化为稳定去重列表。 */
function parseDomainList(value: string): string[] {
  return [...new Set(value.split(/[\n,]/).map((item) => item.trim().toLowerCase().replace(/\.$/, '')).filter(Boolean))].sort();
}

/** formFromConfig 将后台配置投影为只含可编辑字段的表单。 */
function formFromConfig(config: AppConfig): SettingsForm {
  return {
    scheduleEnabled: config.schedule.enabled,
    scheduleInterval: config.schedule.interval,
    runOnNetworkChange: config.schedule.run_on_network_change,
    ipv4: config.benchmark.ipv4,
    ipv6: config.benchmark.ipv6,
    candidates: config.benchmark.candidates,
    concurrency: config.benchmark.concurrency,
    connectAttempts: config.benchmark.connect_attempts,
    lossLimitPercent: config.benchmark.loss_limit * 100,
    switchImprovementPercent: config.benchmark.switch_improvement * 100,
    downloadURL: config.benchmark.download_url,
    tlsServerName: config.benchmark.tls_server_name,
    rangeRefreshInterval: config.ranges.refresh_interval,
    rangeInclude: joinConfigLines(config.ranges.include),
    rangeExclude: joinConfigLines(config.ranges.exclude),
    proxyAutoDetect: Boolean(config.proxy.auto_detect),
    accelerationEnabled: config.acceleration.enabled,
    accelerationManualDomains: joinConfigLines(config.acceleration.manual_domains),
    accelerationExcludedDomains: joinConfigLines(config.acceleration.excluded_domains),
    accelerationAutoDiscover: config.acceleration.auto_discover,
    accelerationAutoApply: config.acceleration.auto_apply,
    accelerationDiscoveryInterval: config.acceleration.discovery_interval,
    networkInterface: config.network.interface,
    gatewayIPv4: config.network.gateway_ipv4,
    gatewayIPv6: config.network.gateway_ipv6,
    manageRoutes: config.network.manage_routes,
  };
}

/** mergeSettings 在保留未展示字段和敏感值的前提下生成完整配置。 */
function mergeSettings(config: AppConfig, form: SettingsForm): AppConfig {
  return {
    ...config,
    schedule: { ...config.schedule, enabled: form.scheduleEnabled, interval: form.scheduleInterval, run_on_network_change: form.runOnNetworkChange },
    ranges: { ...config.ranges, refresh_interval: form.rangeRefreshInterval, include: parseCIDRList(form.rangeInclude), exclude: parseCIDRList(form.rangeExclude) },
    benchmark: {
      ...config.benchmark,
      ipv4: form.ipv4,
      ipv6: form.ipv6,
      candidates: form.candidates,
      concurrency: form.concurrency,
      connect_attempts: form.connectAttempts,
      loss_limit: form.lossLimitPercent / 100,
      switch_improvement: form.switchImprovementPercent / 100,
      download_url: form.downloadURL,
      tls_server_name: form.tlsServerName,
    },
    network: { ...config.network, interface: form.networkInterface.trim(), gateway_ipv4: form.gatewayIPv4.trim(), gateway_ipv6: form.gatewayIPv6.trim(), manage_routes: form.manageRoutes },
    proxy: { ...config.proxy, auto_detect: form.proxyAutoDetect },
    acceleration: {
      ...config.acceleration,
      enabled: form.accelerationEnabled,
      manual_domains: parseDomainList(form.accelerationManualDomains),
      excluded_domains: parseDomainList(form.accelerationExcludedDomains),
      auto_discover: form.accelerationAutoDiscover,
      auto_apply: form.accelerationAutoApply,
      discovery_interval: form.accelerationDiscoveryInterval,
    },
  };
}

/** SettingsPage 编辑经过前端预校验并由后台再次严格校验的运行配置。 */
export function SettingsPage() {
  const config = useConfig();
  const queryClient = useQueryClient();
  const { colorScheme, setColorScheme } = useMantineColorScheme();
  const form = useForm<SettingsForm>({ resolver: zodResolver(settingsSchema), defaultValues: {
    scheduleEnabled: true, scheduleInterval: '6h', runOnNetworkChange: true, ipv4: true, ipv6: true, candidates: 1000, concurrency: 200, connectAttempts: 4, lossLimitPercent: 25, switchImprovementPercent: 15, downloadURL: '', tlsServerName: '', rangeRefreshInterval: '24h', rangeInclude: '', rangeExclude: '', proxyAutoDetect: true, accelerationEnabled: true, accelerationManualDomains: 'ani.momoc.top', accelerationExcludedDomains: '', accelerationAutoDiscover: true, accelerationAutoApply: false, accelerationDiscoveryInterval: '15s', networkInterface: '', gatewayIPv4: '', gatewayIPv6: '', manageRoutes: false,
  } });
  useEffect(() => { if (config.data) form.reset(formFromConfig(config.data)); }, [config.data, form]);
  const save = useMutation({
    mutationFn: (next: AppConfig) => request<{ saved: boolean; restart_required: boolean }>('config.update', { config: next }),
    onSuccess: async (result) => {
      notifications.show({ color: 'green', title: '配置已保存', message: result.restart_required ? '后台服务重启后应用全部更改。' : '更改已经应用。' });
      await queryClient.invalidateQueries({ queryKey: queryKeys.config });
    },
    onError: (error: Error) => notifications.show({ color: 'red', title: '保存失败', message: error.message }),
  });

  if (config.isLoading) return <LoadingState rows={8} />;
  if (config.isError) return <ErrorState message={config.error.message} onRetry={() => config.refetch()} />;
  if (!config.data) return null;
  const errors = form.formState.errors;
  const submit = form.handleSubmit((values) => save.mutate(mergeSettings(config.data!, values)));
  return (
    <form onSubmit={submit}>
      <Stack gap="lg">
        <PageHeader title="设置" description="调度、测速、网段与系统策略配置" actions={<Button type="submit" leftSection={<Save size={16} />} loading={save.isPending} disabled={!form.formState.isDirty}>保存并校验</Button>} />
        <Alert color="yellow" icon={<ShieldAlert size={18} />} title="系统策略边界">启用路由管理后，后台才可能计划系统路由；每次应用仍必须验证并在失败时回滚。UI 始终保持普通权限。</Alert>

        <div className="settings-layout">
          <Stack gap="md">
            <Section title="调度">
              <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="md">
                <Controller control={form.control} name="scheduleInterval" render={({ field }) => <TextInput {...field} label="优选周期" error={errors.scheduleInterval?.message} description="Go duration，例如 6h" />} />
                <div className="switch-stack"><Controller control={form.control} name="scheduleEnabled" render={({ field }) => <Switch label="启用周期任务" checked={field.value} onChange={field.onChange} />} /><Controller control={form.control} name="runOnNetworkChange" render={({ field }) => <Switch label="网络变化时复测" checked={field.value} onChange={field.onChange} />} /></div>
              </SimpleGrid>
            </Section>

            <Section title="测速候选">
              <SimpleGrid cols={{ base: 1, sm: 3 }} spacing="md">
                <Controller control={form.control} name="candidates" render={({ field }) => <NumberInput label="候选数量" min={1} max={100000} value={field.value} onChange={(value) => field.onChange(Number(value))} error={errors.candidates?.message} />} />
                <Controller control={form.control} name="concurrency" render={({ field }) => <NumberInput label="并发数" min={1} max={2000} value={field.value} onChange={(value) => field.onChange(Number(value))} error={errors.concurrency?.message} />} />
                <Controller control={form.control} name="connectAttempts" render={({ field }) => <NumberInput label="连接次数" min={1} max={20} value={field.value} onChange={(value) => field.onChange(Number(value))} error={errors.connectAttempts?.message} />} />
                <Controller control={form.control} name="lossLimitPercent" render={({ field }) => <NumberInput label="丢包上限" suffix="%" min={0} max={100} decimalScale={1} value={field.value} onChange={(value) => field.onChange(Number(value))} error={errors.lossLimitPercent?.message} />} />
                <Controller control={form.control} name="switchImprovementPercent" render={({ field }) => <NumberInput label="切换提升阈值" suffix="%" min={0} max={500} decimalScale={1} value={field.value} onChange={(value) => field.onChange(Number(value))} error={errors.switchImprovementPercent?.message} />} />
                <div className="protocol-options"><Text size="sm" fw={500}>协议族</Text><Group mt={7}><Controller control={form.control} name="ipv4" render={({ field }) => <Checkbox label="IPv4" checked={field.value} onChange={field.onChange} />} /><Controller control={form.control} name="ipv6" render={({ field }) => <Checkbox label="IPv6" checked={field.value} onChange={field.onChange} />} /></Group></div>
              </SimpleGrid>
            </Section>

            <Section title="TLS 与下载复筛">
              <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="md">
                <Controller control={form.control} name="downloadURL" render={({ field }) => <TextInput {...field} label="HTTPS 测速地址" placeholder="留空则不产生下载流量" error={errors.downloadURL?.message} />} />
                <Controller control={form.control} name="tlsServerName" render={({ field }) => <TextInput {...field} label="TLS Server Name" placeholder="与测速域名一致" error={errors.tlsServerName?.message} />} />
              </SimpleGrid>
            </Section>

            <Section title="Cloudflare 网段">
              <Stack gap="md">
                <Controller control={form.control} name="rangeRefreshInterval" render={({ field }) => <TextInput {...field} label="更新周期" error={errors.rangeRefreshInterval?.message} maw={320} />} />
                <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="md">
                  <Controller control={form.control} name="rangeInclude" render={({ field }) => <Textarea {...field} label="包含 CIDR" autosize minRows={4} ff="monospace" placeholder="每行一个 CIDR" />} />
                  <Controller control={form.control} name="rangeExclude" render={({ field }) => <Textarea {...field} label="排除 CIDR" autosize minRows={4} ff="monospace" placeholder="每行一个 CIDR" />} />
                </SimpleGrid>
              </Stack>
            </Section>
          </Stack>

          <Stack gap="md">
            <Section title="加速域名" className="inspector-section">
              <Stack gap="md">
                <Controller control={form.control} name="accelerationEnabled" render={({ field }) => <Switch label="启用 Cloudflare 域名加速" checked={field.value} onChange={field.onChange} />} />
                <Controller control={form.control} name="accelerationManualDomains" render={({ field }) => <Textarea {...field} label="手动域名" autosize minRows={4} ff="monospace" placeholder="每行一个精确域名" />} />
                <Controller control={form.control} name="accelerationExcludedDomains" render={({ field }) => <Textarea {...field} label="排除域名" autosize minRows={3} ff="monospace" placeholder="每行一个精确域名" />} />
                <Controller control={form.control} name="accelerationAutoDiscover" render={({ field }) => <Switch label="自动发现 Cloudflare 域名" checked={field.value} onChange={field.onChange} />} />
                <Controller control={form.control} name="accelerationAutoApply" render={({ field }) => <Switch color="orange" label="自动应用已验证域名" checked={field.value} onChange={field.onChange} />} />
                <Controller control={form.control} name="accelerationDiscoveryInterval" render={({ field }) => <TextInput {...field} label="发现间隔" error={errors.accelerationDiscoveryInterval?.message} />} />
              </Stack>
            </Section>
            <Section title="系统与代理" className="inspector-section">
              <Stack gap="md">
                <Controller control={form.control} name="proxyAutoDetect" render={({ field }) => <Switch label="自动检测代理适配器" checked={field.value} onChange={field.onChange} />} />
                <Controller control={form.control} name="manageRoutes" render={({ field }) => <Switch color="orange" label="允许后台管理系统路由" checked={field.value} onChange={field.onChange} />} />
                <Controller control={form.control} name="networkInterface" render={({ field }) => <TextInput {...field} label="物理接口" placeholder="自动预检失败时填写" error={errors.networkInterface?.message} />} />
                <Controller control={form.control} name="gatewayIPv4" render={({ field }) => <TextInput {...field} label="IPv4 网关" ff="monospace" placeholder="例如 192.168.1.1" error={errors.gatewayIPv4?.message} />} />
                <Controller control={form.control} name="gatewayIPv6" render={({ field }) => <TextInput {...field} label="IPv6 网关" ff="monospace" placeholder="可选" error={errors.gatewayIPv6?.message} />} />
                <Text size="xs" c="dimmed">配置文件由后台原子保存。代理密钥不会通过此表单读回或覆盖。</Text>
              </Stack>
            </Section>
            <Section title="界面主题" className="inspector-section">
              <SegmentedControl fullWidth value={colorScheme} onChange={(value) => setColorScheme(value as 'light' | 'dark' | 'auto')} data={[{ label: '浅色', value: 'light' }, { label: '深色', value: 'dark' }, { label: '跟随系统', value: 'auto' }]} />
            </Section>
            <Section title="配置位置" className="inspector-section">
              <div className="property-grid"><Text c="dimmed">数据目录</Text><Text ff="monospace">{config.data.data_dir}</Text><Text c="dimmed">IPC 端点</Text><Text ff="monospace">{config.data.ipc.endpoint}</Text><Text c="dimmed">配置版本</Text><Text>v{config.data.version}</Text></div>
            </Section>
          </Stack>
        </div>
      </Stack>
    </form>
  );
}
