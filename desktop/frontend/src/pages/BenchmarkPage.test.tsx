import { MantineProvider } from '@mantine/core';
import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { BenchmarkPage } from './BenchmarkPage';

const mocks = vi.hoisted(() => ({
  run: vi.fn(),
  cancel: vi.fn(),
  refetch: vi.fn(),
}));

vi.mock('echarts-for-react', () => ({ default: () => <div data-testid="benchmark-chart" /> }));

vi.mock('../hooks/useRun', () => ({
  useRun: () => ({
    run: mocks.run,
    cancel: mocks.cancel,
    running: false,
    cancelling: false,
  }),
}));

vi.mock('../api/hooks', () => ({
  useStatus: () => ({ data: { policy_available: true, state: { running: false } } }),
  useConfig: () => ({ data: { benchmark: { download_top: 20 } } }),
  useLatestBenchmark: () => ({ data: undefined, isError: false, isFetching: false, refetch: mocks.refetch }),
}));

function renderPage() {
  render(<MantineProvider><BenchmarkPage /></MantineProvider>);
}

describe('BenchmarkPage', () => {
  beforeEach(() => {
    mocks.run.mockReset();
  });

  it('reuses a valid node pool for the normal optimization action', () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: '开始优选' }));
    expect(mocks.run).toHaveBeenCalledWith({ force_range_refresh: false, apply_policy: true });
  });

  it('forces a full node pool refresh without waiting for expiry', () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: '强制刷新节点池' }));
    expect(mocks.run).toHaveBeenCalledWith({ force_range_refresh: true, apply_policy: true });
  });
});
