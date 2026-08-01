import { ActionIcon, Group, Progress, Text, Tooltip } from '@mantine/core';
import { Square } from 'lucide-react';
import type { OptimizerEvent } from '../api/types';

const labels: Record<string, string> = { ranges: '更新网段', benchmark: '测速', tcp: 'TCP 初筛', tls: 'TLS 复筛', download: '下载复筛', selection: '选择节点', complete: '完成' };

export function TaskStrip({ event, cancelling, onCancel }: { event?: OptimizerEvent; cancelling: boolean; onCancel: () => void }) {
  if (!event) return null;
  const progress = event.progress;
  const percent = progress?.total ? Math.round((progress.completed / progress.total) * 100) : 0;
  return (
    <div className="task-strip" role="status">
      <Group gap="sm" wrap="nowrap">
        <span className="activity-dot" />
        <Text size="sm" fw={600}>{labels[event.stage ?? ''] ?? event.message ?? '优选运行中'}</Text>
        {progress && <Text size="xs" c="dimmed" className="tabular">{progress.completed}/{progress.total} · 合格 {progress.qualified}</Text>}
      </Group>
      <Progress value={percent} animated size="sm" aria-label="优选进度" />
      <Tooltip label="取消当前优选">
        <ActionIcon color="red" aria-label="取消优选" loading={cancelling} onClick={onCancel}><Square size={15} fill="currentColor" /></ActionIcon>
      </Tooltip>
    </div>
  );
}
