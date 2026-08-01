import { Badge, type MantineColor } from '@mantine/core';
import { CircleAlert, CircleCheck, CircleDashed, CircleX, RotateCcw, ShieldAlert } from 'lucide-react';

type Tone = 'verified' | 'pending' | 'failed' | 'rolled-back' | 'warning' | 'neutral';

const styles: Record<Tone, { color: MantineColor; icon: typeof CircleCheck }> = {
  verified: { color: 'green', icon: CircleCheck },
  pending: { color: 'blue', icon: CircleDashed },
  failed: { color: 'red', icon: CircleX },
  'rolled-back': { color: 'gray', icon: RotateCcw },
  warning: { color: 'yellow', icon: ShieldAlert },
  neutral: { color: 'gray', icon: CircleAlert },
};

export function StatusBadge({ label, tone = 'neutral' }: { label: string; tone?: Tone }) {
  const style = styles[tone];
  const Icon = style.icon;
  return <Badge color={style.color} variant="light" leftSection={<Icon size={12} strokeWidth={2} />}>{label}</Badge>;
}
