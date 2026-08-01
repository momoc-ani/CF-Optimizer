import { describe, expect, it } from 'vitest';
import { redactLogLine } from './redact';

describe('redactLogLine', () => {
  it('removes structured secrets and bearer tokens', () => {
    const input = '{"token":"abc123","authorization":"Bearer header.payload.signature","msg":"safe"}';
    const result = redactLogLine(input);
    expect(result).not.toContain('abc123');
    expect(result).not.toContain('header.payload.signature');
    expect(result).toContain('[REDACTED]');
    expect(result).toContain('safe');
  });

  it('keeps ordinary diagnostic fields unchanged', () => {
    const input = '{"target_ip":"104.16.1.2","result":"verified"}';
    expect(redactLogLine(input)).toBe(input);
  });
});
