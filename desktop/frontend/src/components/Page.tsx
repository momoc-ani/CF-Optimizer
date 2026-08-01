import type { ReactNode } from 'react';
import { ActionIcon, Box, Group, Skeleton, Stack, Text, Title, Tooltip } from '@mantine/core';
import { Inbox, RotateCw } from 'lucide-react';

export function PageHeader({ title, description, actions }: { title: string; description?: string; actions?: ReactNode }) {
  return (
    <Group justify="space-between" align="flex-start" wrap="nowrap" className="page-header">
      <Box>
        <Title order={1}>{title}</Title>
        {description && <Text c="dimmed" size="sm" mt={3}>{description}</Text>}
      </Box>
      {actions && <Group gap="xs" wrap="nowrap">{actions}</Group>}
    </Group>
  );
}

export function Section({ title, aside, children, className = '' }: { title?: string; aside?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={`content-section ${className}`}>
      {(title || aside) && <Group justify="space-between" mb="sm"><Text fw={650}>{title}</Text>{aside}</Group>}
      {children}
    </section>
  );
}

export function Metric({ label, value, detail, accent }: { label: string; value: ReactNode; detail?: ReactNode; accent?: string }) {
  return (
    <div className="metric" style={accent ? { borderTopColor: accent } : undefined}>
      <Text size="xs" c="dimmed">{label}</Text>
      <div className="metric-value">{value}</div>
      {detail && <Text size="xs" c="dimmed" lineClamp={2}>{detail}</Text>}
    </div>
  );
}

export function LoadingState({ rows = 4 }: { rows?: number }) {
  return <Stack gap="sm" aria-label="正在加载">{Array.from({ length: rows }).map((_, index) => <Skeleton key={index} height={38} radius="sm" />)}</Stack>;
}

export function EmptyState({ title, detail, action }: { title: string; detail: string; action?: ReactNode }) {
  return (
    <div className="empty-state">
      <Inbox size={28} />
      <Text fw={600}>{title}</Text>
      <Text c="dimmed" size="sm">{detail}</Text>
      {action}
    </div>
  );
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="empty-state error-state" role="alert">
      <Text fw={600}>读取失败</Text>
      <Text c="dimmed" size="sm">{message}</Text>
      {onRetry && <Tooltip label="重新请求后台服务"><ActionIcon aria-label="重新请求" onClick={onRetry}><RotateCw size={17} /></ActionIcon></Tooltip>}
    </div>
  );
}
