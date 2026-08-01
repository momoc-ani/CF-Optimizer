import { Alert, Button, Group, Modal, SegmentedControl, Stack, Text, ThemeIcon } from '@mantine/core';
import { Gauge, RotateCcw, Settings2, ShieldCheck } from 'lucide-react';
import type { QuickStartMode, QuickStartPlan } from '../api/types';

const effectLabels: Record<string, string> = {
  system_routes: '系统主机路由',
  mihomo_policy: 'Mihomo 直连策略',
  sing_box_policy: 'sing-box 直连策略',
  xray_policy: 'Xray 直连策略',
  external_policy: '外部适配器策略',
  windows_hosts: 'Windows Hosts 受管区块',
};

interface QuickStartDialogProps {
  opened: boolean;
  plan?: QuickStartPlan;
  mode: QuickStartMode;
  running: boolean;
  onModeChange: (mode: QuickStartMode) => void;
  onClose: () => void;
  onConfirm: () => void;
  onBenchmarkOnly: () => void;
  onAdvanced: () => void;
}

/** QuickStartDialog 只展示服务端签发计划允许确认的影响范围。 */
export function QuickStartDialog({ opened, plan, mode, running, onModeChange, onClose, onConfirm, onBenchmarkOnly, onAdvanced }: QuickStartDialogProps) {
  const path = plan?.physical_path;
  return (
    <Modal opened={opened} onClose={onClose} title="确认一键优选" centered size="lg" closeOnClickOutside={!running} closeOnEscape={!running}>
      {plan && (
        <Stack gap="md">
          {plan.can_apply ? (
            <Alert color="blue" icon={<ShieldCheck size={18} />} title="物理出口已完成只读预检">
              确认后后台将测速、应用策略并核对实际接口与网关；验证失败时按事务回滚。
            </Alert>
          ) : (
            <Alert color="yellow" icon={<Settings2 size={18} />} title="需要人工确认物理出口">
              当前计划不会修改系统策略。可以先完成仅测速，或在高级设置中补充接口与网关。
            </Alert>
          )}

          <div className="quickstart-summary">
            <Text c="dimmed" size="sm">物理接口</Text><Text ff="monospace" fw={600}>{path?.interface || '未发现'}</Text>
            <Text c="dimmed" size="sm">IPv4 网关</Text><Text ff="monospace">{path?.gateway_ipv4 || '—'}</Text>
            <Text c="dimmed" size="sm">IPv6 网关</Text><Text ff="monospace">{path?.gateway_ipv6 || '—'}</Text>
            <Text c="dimmed" size="sm">影响范围</Text><Group gap={6}>{plan.effects.length ? plan.effects.map((effect) => <Text size="sm" key={effect}>{effectLabels[effect] || effect}</Text>) : <Text size="sm">无可应用策略</Text>}</Group>
          </div>

          {(plan.warnings ?? []).map((warning) => <Alert key={warning} color="yellow" py="xs">{warning}</Alert>)}

          {plan.can_apply && (
            <Stack gap={6}>
              <Text size="sm" fw={600}>运行方式</Text>
              <SegmentedControl
                fullWidth
                value={mode}
                onChange={(value) => onModeChange(value as QuickStartMode)}
                data={[{ label: '仅本次应用', value: 'apply_once' }, { label: '以后自动维护', value: 'apply_and_remember' }]}
              />
              <Group gap="xs" wrap="nowrap"><ThemeIcon color="gray" variant="light" size="sm"><RotateCcw size={14} /></ThemeIcon><Text size="xs" c="dimmed">所有修改都有事务记录；未通过验证时恢复原策略。</Text></Group>
            </Stack>
          )}

          <Group justify="space-between" mt="xs">
            <Button variant="subtle" color="gray" leftSection={<Settings2 size={16} />} onClick={onAdvanced}>高级设置</Button>
            <Group gap="xs">
              <Button variant="default" onClick={onClose} disabled={running}>取消</Button>
              {plan.can_apply
                ? <Button leftSection={<ShieldCheck size={16} />} loading={running} onClick={onConfirm}>开始并验证</Button>
                : <Button leftSection={<Gauge size={16} />} loading={running} onClick={onBenchmarkOnly}>仅测速</Button>}
            </Group>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}
