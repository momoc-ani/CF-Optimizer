import { notifications } from '@mantine/notifications';
import { render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { PolicyGuardStatus } from '../api/types';
import { PolicyGuardNotifications } from './PolicyGuardNotifications';

const verified: PolicyGuardStatus = {
  id: 'mihomo', state: 'verified', online: true, activity: 'active', system_proxy_active: true,
  tun_active: false, manageable: true, transition: 1, message: '已验证',
};

describe('PolicyGuardNotifications', () => {
  afterEach(() => vi.restoreAllMocks());

  it('只在守护状态迁移时通知一次', () => {
    const show = vi.spyOn(notifications, 'show').mockImplementation(() => 'test-notification');
    const view = render(<PolicyGuardNotifications guards={{ mihomo: verified }} />);
    expect(show).not.toHaveBeenCalled();

    const drifted = { ...verified, state: 'drifted' as const, transition: 2, message: '规则已被覆盖' };
    view.rerender(<PolicyGuardNotifications guards={{ mihomo: drifted }} />);
    expect(show).toHaveBeenCalledTimes(1);

    view.rerender(<PolicyGuardNotifications guards={{ mihomo: drifted }} />);
    expect(show).toHaveBeenCalledTimes(1);
  });
});
