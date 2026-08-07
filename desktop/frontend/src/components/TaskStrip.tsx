import { ActionIcon, Group, Progress, Text, Tooltip } from '@mantine/core';
import { Square } from 'lucide-react';
import type { OptimizerEvent } from '../api/types';

const labels: Record<string, string> = { ranges: '更新网段', pool_refresh: '刷新测速节点池', pool_reuse: '复用并校验节点池', domain_qualify: '校验域名映射', policy_plan: '生成策略计划', apply_verify: '应用并验证策略', commit: '提交优选结果', benchmark: '测速', tcp: 'TCP 初筛', tls: 'TLS 复筛', download: '下载复筛', selection: '选择节点', policy: '应用并验证策略', config: '更新配置并刷新策略', complete: '任务收尾' };

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
