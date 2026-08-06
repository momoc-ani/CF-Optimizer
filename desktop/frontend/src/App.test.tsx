import { describe, expect, it } from 'vitest';
import type { OptimizerEvent, SystemStatus } from './api/types';
import { selectActiveTaskEvent } from './lib/activeTask';

const backgroundEvent: OptimizerEvent = {
  run_id: 'config-update-1',
  type: 'stage.started',
  stage: 'config',
  message: 'updating configuration and refreshing verified policy',
  timestamp: '2026-08-06T13:30:00Z',
};

describe('selectActiveTaskEvent', () => {
  it('restores a background configuration refresh when no local mutation is pending', () => {
    const status = { active_event: backgroundEvent } as SystemStatus;
    expect(selectActiveTaskEvent(false, undefined, status)).toBe(backgroundEvent);
  });

  it('prefers the task started by the current window', () => {
    const localEvent = { ...backgroundEvent, run_id: 'local-run', stage: 'benchmark' };
    const status = { active_event: backgroundEvent } as SystemStatus;
    expect(selectActiveTaskEvent(true, localEvent, status)).toBe(localEvent);
  });
});
