import { MantineProvider } from '@mantine/core';
import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { SystemStatus } from '../api/types';
import { Shell } from './Shell';

describe('Shell', () => {
  it('shows recovery state while the daemon is connected but not ready', () => {
    const status = {
      state: { running: false },
      startup: { ready: false, stage: 'recovering_routes', message: '正在恢复中断的路由事务' },
      build: { version: 'test' },
    } as SystemStatus;

    render(<MantineProvider><Shell status={status} disconnected={false}><div>content</div></Shell></MantineProvider>);

    expect(screen.getByText('后台恢复中')).toBeVisible();
  });
});
