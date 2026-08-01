import { createContext } from 'react';
import type { OptimizerEvent, QuickStartMode, QuickStartResult, RunReport } from '../api/types';

export interface RunOptions extends Record<string, unknown> {
  force_range_refresh: boolean;
  apply_policy: boolean;
}

export interface QuickStartRunOptions extends Record<string, unknown> {
  plan_id: string;
  mode: QuickStartMode;
  force_range_refresh: boolean;
}

export interface RunContextValue {
  event?: OptimizerEvent;
  report?: RunReport;
  quickStartResult?: QuickStartResult;
  running: boolean;
  cancelling: boolean;
  error?: Error;
  run: (options: RunOptions) => Promise<RunReport>;
  runQuickStart: (options: QuickStartRunOptions) => Promise<QuickStartResult>;
  cancel: () => void;
}

/** RunContext 保存当前 UI 会话观察到的后台优选状态。 */
export const RunContext = createContext<RunContextValue | null>(null);
