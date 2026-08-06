import { Alert, Button, Group, Select, Stack, Text, TextInput } from '@mantine/core';
import { useMutation } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { Download, RefreshCw, ScanSearch, Search, ShieldAlert, ShieldCheck } from 'lucide-react';
import { useMemo, useState } from 'react';
import { request } from '../api/client';
import { useLogs } from '../api/hooks';
import type { DiagnosticReport } from '../api/types';
import { DataTable } from '../components/DataTable';
import { ErrorState, LoadingState, PageHeader, Section } from '../components/Page';
import { StatusBadge } from '../components/StatusBadge';
import { routeDiagnosticPresentation } from '../lib/diagnostics';
import { formatDate } from '../lib/format';
import { sortLogsNewestFirst } from '../lib/logs';
import { redactLogLine } from '../lib/redact';
import { useUIStore } from '../state/ui';

interface LogEntry {
  raw: string;
  time?: string;
  level: string;
  component: string;
  msg: string;
  run_id?: string;
  transaction_id?: string;
  [key: string]: unknown;
}

/** parseLogLine 保留无法解析的日志行，避免诊断页面因单条损坏记录白屏。 */
function parseLogLine(raw: string): LogEntry {
  try {
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    return { ...parsed, raw, time: typeof parsed.time === 'string' ? parsed.time : undefined, level: String(parsed.level ?? 'INFO').toUpperCase(), component: String(parsed.component ?? 'system'), msg: String(parsed.msg ?? raw) };
  } catch {
    return { raw, level: 'INFO', component: 'legacy', msg: raw };
  }
}

/** logTone 将日志级别映射为语义状态。 */
function logTone(level: string): 'failed' | 'warning' | 'neutral' | 'verified' {
  if (level === 'ERROR') return 'failed';
  if (level === 'WARN' || level === 'WARNING') return 'warning';
  if (level === 'INFO') return 'verified';
  return 'neutral';
}

/** LogsPage 提供结构化日志筛选、脱敏导出和按目标路由诊断。 */
export function LogsPage() {
  const [lineCount, setLineCount] = useState('500');
  const logs = useLogs(Number(lineCount));
  const search = useUIStore((state) => state.logSearch);
  const setSearch = useUIStore((state) => state.setLogSearch);
  const [level, setLevel] = useState('all');
  const [target, setTarget] = useState('104.16.132.229');
  const diagnostic = useMutation({ mutationFn: () => request<DiagnosticReport>('diagnostics.route', { target: target.trim() }) });
  const entries = useMemo(() => sortLogsNewestFirst((logs.data ?? []).map(parseLogLine)), [logs.data]);
  const filtered = useMemo(() => entries.filter((entry) => (level === 'all' || entry.level === level) && `${entry.time ?? ''} ${entry.level} ${entry.component} ${entry.msg} ${entry.run_id ?? ''} ${entry.transaction_id ?? ''}`.toLowerCase().includes(search.toLowerCase())), [entries, level, search]);
  const columns = useMemo<ColumnDef<LogEntry>[]>(() => [
    { accessorKey: 'time', header: '时间', size: 160, cell: ({ getValue }) => formatDate(String(getValue() ?? '')) },
    { accessorKey: 'level', header: '级别', size: 92, cell: ({ getValue }) => <StatusBadge label={String(getValue())} tone={logTone(String(getValue()))} /> },
    { accessorKey: 'component', header: '组件', size: 120, cell: ({ getValue }) => <Text ff="monospace" size="sm">{String(getValue())}</Text> },
    { accessorKey: 'msg', header: '消息', size: 430, cell: ({ getValue }) => <Text size="sm">{String(getValue())}</Text> },
    { id: 'correlation', header: '关联 ID', size: 220, accessorFn: (row) => row.run_id ?? row.transaction_id ?? '', cell: ({ getValue }) => <Text ff="monospace" size="xs" c="dimmed">{String(getValue() || '—')}</Text> },
  ], []);

  /** exportLogs 生成只包含当前筛选记录的 UTF-8 脱敏诊断文件。 */
  const exportLogs = () => {
    const content = filtered.map((entry) => redactLogLine(entry.raw)).join('\n');
    const url = URL.createObjectURL(new Blob([content], { type: 'application/x-ndjson;charset=utf-8' }));
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = `cf-optimizer-logs-${new Date().toISOString().replace(/[:.]/g, '-')}.jsonl`;
    anchor.click();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
  };

  if (logs.isLoading) return <LoadingState rows={7} />;
  if (logs.isError) return <ErrorState message={logs.error.message} onRetry={() => logs.refetch()} />;
  const report = diagnostic.data;
  const presentation = report ? routeDiagnosticPresentation(report) : undefined;
  return (
    <Stack gap="lg">
      <PageHeader title="日志诊断" description="结构化运行日志、关联事务和按目标直连诊断" actions={<Group gap="xs"><Button variant="light" leftSection={<RefreshCw size={16} />} loading={logs.isFetching} onClick={() => logs.refetch()}>刷新</Button><Button variant="light" leftSection={<Download size={16} />} disabled={!filtered.length} onClick={exportLogs}>导出日志</Button></Group>} />
      <div className="logs-layout">
        <Section title="诊断目标" className="inspector-section">
          <Stack gap="md">
            <TextInput label="Cloudflare 目标 IP" value={target} onChange={(event) => { setTarget(event.currentTarget.value); diagnostic.reset(); }} ff="monospace" />
            <Button leftSection={<ScanSearch size={16} />} loading={diagnostic.isPending} disabled={!target.trim()} onClick={() => diagnostic.mutate()}>运行诊断</Button>
            {diagnostic.isError && <Alert color="red" title="诊断失败">{diagnostic.error.message}</Alert>}
            {presentation && <Alert color={presentation.verified ? 'green' : 'yellow'} icon={presentation.verified ? <ShieldCheck size={17} /> : <ShieldAlert size={17} />} title={presentation.verified ? '证据已验证' : '证据不足'}>{presentation.detail}</Alert>}
            {(report?.warnings ?? []).map((warning) => <Text key={warning} size="xs" c="orange">{warning}</Text>)}
          </Stack>
        </Section>
        <Section title="日志流">
          <Group mb="sm" align="flex-end">
            <TextInput label="搜索" leftSection={<Search size={15} />} placeholder="消息、Run ID、事务 ID" value={search} onChange={(event) => setSearch(event.currentTarget.value)} className="grow-control" />
            <Select label="级别" value={level} onChange={(value) => setLevel(value ?? 'all')} data={[{ label: '全部', value: 'all' }, { label: 'INFO', value: 'INFO' }, { label: 'WARN', value: 'WARN' }, { label: 'ERROR', value: 'ERROR' }, { label: 'DEBUG', value: 'DEBUG' }]} w={120} />
            <Select label="读取行数" value={lineCount} onChange={(value) => setLineCount(value ?? '500')} data={['200', '500', '1000', '2000']} w={120} />
          </Group>
          <div className="logs-table-window">
            <DataTable columns={columns} data={filtered} minWidth={1030} rowKey={(row, index) => `${row.time ?? 'log'}-${index}`} emptyTitle="没有匹配日志" emptyDetail={entries.length ? '调整级别或搜索条件。' : '日志文件尚未产生记录。'} footer={`显示 ${filtered.length} / ${entries.length} 条；导出时再次过滤认证字段。`} />
          </div>
        </Section>
      </div>
    </Stack>
  );
}
