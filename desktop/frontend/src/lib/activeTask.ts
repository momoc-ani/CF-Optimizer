import type { OptimizerEvent, SystemStatus } from '../api/types';

/** selectActiveTaskEvent 优先展示本窗口任务，并在重连后恢复后台维护任务。 */
export function selectActiveTaskEvent(localRunning: boolean, localEvent: OptimizerEvent | undefined, status: SystemStatus | undefined): OptimizerEvent | undefined {
  return localRunning ? localEvent : status?.active_event;
}
