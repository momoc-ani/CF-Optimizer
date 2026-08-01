import { mockRequest, subscribeMockEvents } from './mock';
import type { OptimizerEvent } from './types';

declare global {
  interface Window {
    go?: { desktop?: { Bridge?: { Request(method: string, parameters: Record<string, unknown>): Promise<string> } } };
    runtime?: { EventsOn(name: string, callback: (payload: string) => void): () => void };
  }
}

export const isWails = () => Boolean(window.go?.desktop?.Bridge?.Request);

/** normalizeBridgeError 将 Wails 的字符串拒绝值统一为前端可安全处理的 Error。 */
export function normalizeBridgeError(value: unknown): Error {
  if (value instanceof Error) return value;
  if (typeof value === 'string') return new Error(value);
  if (value && typeof value === 'object' && 'message' in value && typeof value.message === 'string') {
    return new Error(value.message);
  }
  return new Error(String(value ?? 'unknown desktop bridge error'));
}

/** request 调用桌面桥接器，并统一成功载荷和失败值的运行时类型。 */
export async function request<T>(method: string, parameters: Record<string, unknown> = {}): Promise<T> {
  const bridge = window.go?.desktop?.Bridge;
  if (!bridge) return mockRequest<T>(method, parameters);
  try {
    const raw = await bridge.Request(method, parameters);
    return JSON.parse(raw) as T;
  } catch (error) {
    throw normalizeBridgeError(error);
  }
}

export function subscribeOptimizerEvents(listener: (event: OptimizerEvent) => void): () => void {
  if (!window.runtime?.EventsOn) return subscribeMockEvents(listener);
  return window.runtime.EventsOn('optimizer:event', (payload) => listener(JSON.parse(payload) as OptimizerEvent));
}
