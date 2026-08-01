import { mockRequest, subscribeMockEvents } from './mock';
import type { OptimizerEvent } from './types';

declare global {
  interface Window {
    go?: { desktop?: { Bridge?: { Request(method: string, parameters: Record<string, unknown>): Promise<string> } } };
    runtime?: { EventsOn(name: string, callback: (payload: string) => void): () => void };
  }
}

export const isWails = () => Boolean(window.go?.desktop?.Bridge?.Request);

export async function request<T>(method: string, parameters: Record<string, unknown> = {}): Promise<T> {
  const bridge = window.go?.desktop?.Bridge;
  if (!bridge) return mockRequest<T>(method, parameters);
  const raw = await bridge.Request(method, parameters);
  return JSON.parse(raw) as T;
}

export function subscribeOptimizerEvents(listener: (event: OptimizerEvent) => void): () => void {
  if (!window.runtime?.EventsOn) return subscribeMockEvents(listener);
  return window.runtime.EventsOn('optimizer:event', (payload) => listener(JSON.parse(payload) as OptimizerEvent));
}
