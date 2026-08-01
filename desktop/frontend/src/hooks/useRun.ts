import { useContext } from 'react';
import { RunContext } from '../state/run';

/** useRun 读取后台优选任务的界面上下文。 */
export function useRun() {
  const value = useContext(RunContext);
  if (!value) throw new Error('useRun must be used inside RunProvider');
  return value;
}
