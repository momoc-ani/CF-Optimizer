import { Alert, Button, Group, SimpleGrid, Stack, Table, Text } from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { CloudDownload, Database, RefreshCw, ShieldCheck } from 'lucide-react';
import { request } from '../api/client';
import { queryKeys, useRanges } from '../api/hooks';
import type { RangeSnapshot } from '../api/types';
import { ErrorState, LoadingState, Metric, PageHeader, Section } from '../components/Page';
import { StatusBadge } from '../components/StatusBadge';
import { formatDate, shortHash } from '../lib/format';

interface RangeUpdateResult {
  snapshot: RangeSnapshot;
  updated: boolean;
  warning?: string;
}

/** RangeList 以紧凑、可横向滚动的列表展示 CIDR。 */
function RangeList({ title, ranges }: { title: string; ranges: string[] }) {
  return (
    <Section title={title} aside={<Text size="xs" c="dimmed">{ranges.length} 条</Text>}>
      <div className="range-list">
        <Table verticalSpacing="xs" withRowBorders>
          <Table.Thead><Table.Tr><Table.Th>网段</Table.Th><Table.Th>状态</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>{ranges.map((range) => <Table.Tr key={range}><Table.Td><Text ff="monospace" size="sm">{range}</Text></Table.Td><Table.Td><StatusBadge label="官方快照" tone="verified" /></Table.Td></Table.Tr>)}</Table.Tbody>
        </Table>
      </div>
    </Section>
  );
}

/** RangesPage 展示已验证网段快照并触发后台安全更新。 */
export function RangesPage() {
  const ranges = useRanges();
  const queryClient = useQueryClient();
  const update = useMutation({
    mutationFn: () => request<RangeUpdateResult>('ranges.update'),
    onSuccess: async (result) => {
      queryClient.setQueryData(queryKeys.ranges, result.snapshot);
      notifications.show({ color: result.warning ? 'yellow' : 'green', title: result.warning ? '继续使用有效快照' : result.updated ? '网段已更新' : '网段无需更新', message: result.warning ?? (result.updated ? '新的官方快照已通过校验并原子替换。' : '本地快照仍是最新版本。') });
      await ranges.refetch();
    },
    onError: (error: Error) => notifications.show({ color: 'red', title: '网段更新失败', message: error.message }),
  });

  if (ranges.isLoading) return <LoadingState rows={7} />;
  if (ranges.isError) return <ErrorState message={ranges.error.message} onRetry={() => ranges.refetch()} />;
  const snapshot = ranges.data;
  if (!snapshot) return null;
  const total = snapshot.ipv4.length + snapshot.ipv6.length;
  return (
    <Stack gap="lg">
      <PageHeader title="网段管理" description="Cloudflare 官方来源、快照校验与自定义范围" actions={<Button leftSection={<CloudDownload size={16} />} loading={update.isPending} onClick={() => update.mutate()}>检查更新</Button>} />
      <SimpleGrid cols={{ base: 2, md: 4 }} spacing="sm">
        <Metric label="有效网段" value={total} detail={`${snapshot.ipv4.length} IPv4 · ${snapshot.ipv6.length} IPv6`} accent="#1677a6" />
        <Metric label="数据来源" value={snapshot.source} detail={snapshot.etag ?? '无 ETag'} accent="#2b8a5a" />
        <Metric label="更新时间" value={formatDate(snapshot.fetched_at)} detail="运行中的任务持有不可变快照" />
        <Metric label="内容哈希" value={<Text ff="monospace" inherit>{shortHash(snapshot.hash)}</Text>} detail={`快照版本 v${snapshot.version}`} accent="#7950f2" />
      </SimpleGrid>

      <Alert color="blue" icon={<ShieldCheck size={18} />} title="当前快照已通过后台校验">
        异常远程数据不会覆盖最后一份有效快照；自定义排除项在候选生成前应用。
      </Alert>

      <div className="ranges-grid">
        <div className="range-columns">
          <RangeList title="IPv4 CIDR" ranges={snapshot.ipv4} />
          <RangeList title="IPv6 CIDR" ranges={snapshot.ipv6} />
        </div>
        <Stack gap="md">
          <Section title="自定义范围" className="inspector-section">
            <Stack gap="md">
              <div><Group justify="space-between"><Text fw={600}>包含</Text><StatusBadge label={`+${snapshot.include?.length ?? 0}`} tone="neutral" /></Group>{(snapshot.include ?? []).length ? snapshot.include?.map((item) => <Text key={item} mt="xs" ff="monospace" size="sm">{item}</Text>) : <Text mt="xs" size="sm" c="dimmed">无自定义包含网段</Text>}</div>
              <div><Group justify="space-between"><Text fw={600}>排除</Text><StatusBadge label={`-${snapshot.exclude?.length ?? 0}`} tone={(snapshot.exclude?.length ?? 0) ? 'warning' : 'neutral'} /></Group>{(snapshot.exclude ?? []).length ? snapshot.exclude?.map((item) => <Text key={item} mt="xs" ff="monospace" size="sm">{item}</Text>) : <Text mt="xs" size="sm" c="dimmed">无自定义排除网段</Text>}</div>
              <Text size="xs" c="dimmed">范围编辑在“设置”保存，并由后台进行 CIDR、安全地址类别与变化比例校验。</Text>
            </Stack>
          </Section>
          <Section title="快照存储" className="inspector-section">
            <Group gap="sm" wrap="nowrap"><Database size={19} /><div><Text fw={600}>双版本原子快照</Text><Text size="sm" c="dimmed">当前版本与上一有效版本均由后台维护。</Text></div></Group>
          </Section>
          <Button variant="light" leftSection={<RefreshCw size={16} />} loading={ranges.isFetching} onClick={() => ranges.refetch()}>重新读取本地快照</Button>
        </Stack>
      </div>
    </Stack>
  );
}
