import { afterEach, describe, expect, it, vi } from 'vitest';
import { normalizeBridgeError, request } from './client';

afterEach(() => {
  delete window.go;
});

describe('desktop bridge client', () => {
  it('normalizes a Wails string rejection to Error', async () => {
    window.go = {
      desktop: {
        Bridge: {
          Request: vi.fn().mockRejectedValue('route verification failed'),
        },
      },
    };

    await expect(request('quickstart.run')).rejects.toThrow('route verification failed');
  });

  it('preserves an existing Error instance', () => {
    const source = new Error('request failed');
    expect(normalizeBridgeError(source)).toBe(source);
  });
});
