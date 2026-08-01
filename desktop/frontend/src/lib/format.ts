export function formatDuration(value?: number): string {
  if (!value) return '—';
  return `${(value / 1_000_000).toFixed(1)} ms`;
}

export function formatPercent(value?: number): string {
  if (value === undefined) return '—';
  return `${(value * 100).toFixed(1)}%`;
}

export function formatScore(value?: number): string {
  if (value === undefined) return '—';
  return value.toFixed(1);
}

export function formatMbps(value?: number): string {
  if (!value) return '—';
  return `${value.toFixed(1)} Mbps`;
}

export function formatDate(value?: string): string {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(date);
}

export function shortHash(value?: string): string {
  if (!value) return '—';
  return value.slice(0, 12);
}
