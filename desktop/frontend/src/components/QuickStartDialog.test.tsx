import { MantineProvider } from '@mantine/core';
import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { QuickStartPlan } from '../api/types';
import { QuickStartDialog } from './QuickStartDialog';

const readyPlan: QuickStartPlan = {
  plan_id: 'plan-test', expires_at: '2026-08-01T12:05:00Z',
  physical_path: { interface: 'Ethernet 2', gateway_ipv4: '192.168.50.1' },
  effects: ['system_routes', 'mihomo_policy'], detections: {}, can_apply: true,
  manual_required: false, auto_maintenance_enabled: false,
};

function renderDialog(plan: QuickStartPlan, overrides: Partial<React.ComponentProps<typeof QuickStartDialog>> = {}) {
  const props: React.ComponentProps<typeof QuickStartDialog> = {
    opened: true, plan, mode: 'apply_once', running: false,
    onModeChange: vi.fn(), onClose: vi.fn(), onConfirm: vi.fn(), onBenchmarkOnly: vi.fn(), onAdvanced: vi.fn(),
    ...overrides,
  };
  render(<MantineProvider><QuickStartDialog {...props} /></MantineProvider>);
  return props;
}

describe('QuickStartDialog', () => {
  it('shows the confirmed path and starts apply-once mode', () => {
    const props = renderDialog(readyPlan);
    expect(screen.getByText('Ethernet 2')).toBeVisible();
    expect(screen.getByText('192.168.50.1')).toBeVisible();
    expect(screen.getByText('系统主机路由')).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '开始并验证' }));
    expect(props.onConfirm).toHaveBeenCalledOnce();
  });

  it('allows choosing persistent automatic maintenance', () => {
    const onModeChange = vi.fn();
    renderDialog(readyPlan, { onModeChange });
    fireEvent.click(screen.getByText('以后自动维护'));
    expect(onModeChange).toHaveBeenCalledWith('apply_and_remember');
  });

  it('only offers benchmark and advanced settings when discovery fails', () => {
    const onBenchmarkOnly = vi.fn();
    const onAdvanced = vi.fn();
    renderDialog({ ...readyPlan, can_apply: false, manual_required: true, effects: [], warnings: ['未发现可信网关'] }, { onBenchmarkOnly, onAdvanced });
    expect(screen.queryByRole('button', { name: '开始并验证' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '仅测速' }));
    fireEvent.click(screen.getByRole('button', { name: '高级设置' }));
    expect(onBenchmarkOnly).toHaveBeenCalledOnce();
    expect(onAdvanced).toHaveBeenCalledOnce();
  });
});
