import { describe, expect, it } from 'vitest';
import { formatDuration, formatMbps, formatPercent, formatScore, shortHash } from './format';

describe('format helpers', () => {
  it('formats benchmark values with stable units', () => {
    expect(formatDuration(31_800_000)).toBe('31.8 ms');
    expect(formatMbps(184.74)).toBe('184.7 Mbps');
    expect(formatPercent(0.25)).toBe('25.0%');
    expect(formatScore(94.64)).toBe('94.6');
  });

  it('handles missing values and short hashes', () => {
    expect(formatDuration()).toBe('—');
    expect(formatMbps()).toBe('—');
    expect(shortHash('7498fd8cf1742d5ace97')).toBe('7498fd8cf174');
  });
});
