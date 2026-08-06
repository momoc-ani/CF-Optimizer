import { notifications } from '@mantine/notifications';
import { useEffect, useRef } from 'react';
import type { PolicyGuardState, PolicyGuardStatus } from '../api/types';

/** PolicyGuardNotifications 仅在守护状态真正迁移时提示，避免轮询产生重复通知。 */
export function PolicyGuardNotifications({ guards }: { guards?: Record<string, PolicyGuardStatus> }) {
  const transitions = useRef<Record<string, number>>({});

  useEffect(() => {
    for (const [id, guard] of Object.entries(guards ?? {})) {
      const previous = transitions.current[id];
      transitions.current[id] = guard.transition;
      if (previous === undefined) {
        if (guard.state === 'failed') showGuardNotification(id, guard);
        continue;
      }
      if (previous === guard.transition) continue;
      if (['drifted', 'restoring', 'verified', 'retry_wait', 'failed'].includes(guard.state)) {
        showGuardNotification(id, guard);
      }
    }
  }, [guards]);

  return null;
}

function showGuardNotification(id: string, guard: PolicyGuardStatus) {
  const presentations: Partial<Record<PolicyGuardState, { color: 'blue' | 'yellow' | 'green' | 'red'; title: string }>> = {
    restoring: { color: 'blue', title: `${id} 正在恢复规则` },
    drifted: { color: 'yellow', title: `${id} 规则已被覆盖` },
    verified: { color: 'green', title: `${id} 直连规则已验证` },
    retry_wait: { color: 'yellow', title: `${id} 规则恢复将重试` },
    failed: { color: 'red', title: `${id} 规则恢复失败` },
  };
  const presentation = presentations[guard.state];
  if (presentation) notifications.show({ ...presentation, message: guard.message ?? '查看代理适配页面了解详情。' });
}
