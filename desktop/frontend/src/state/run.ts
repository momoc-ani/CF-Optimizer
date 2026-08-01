import { createContext } from 'react';
import type { OptimizerEvent, RunReport } from '../api/types';

export interface RunOptions extends Record<string, unknown> {
  force_range_refresh: boolean;
  apply_policy: boolean;
}

export interface RunContextValue {
  event?: OptimizerEvent;
  report?: RunReport;
  running: boolean;
  cancelling: boolean;
  error?: Error;
  run: (options: RunOptions) => void;
  cancel: () => void;
}

/** RunContext 保存当前 UI 会话观察到的后台优选状态。 */
export const RunContext = createContext<RunContextValue | null>(null);
