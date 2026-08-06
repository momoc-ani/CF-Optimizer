import { describe, expect, it } from 'vitest';
import { sortLogsNewestFirst } from './logs';

describe('sortLogsNewestFirst', () => {
  it('puts the newest timestamp first while preserving equal-time order', () => {
    const entries = [
      { id: 'old', time: '2026-08-06T09:00:00Z' },
      { id: 'same-a', time: '2026-08-06T10:00:00Z' },
      { id: 'same-b', time: '2026-08-06T10:00:00Z' },
      { id: 'new', time: '2026-08-06T11:00:00Z' },
    ];

    expect(sortLogsNewestFirst(entries).map((entry) => entry.id)).toEqual(['new', 'same-a', 'same-b', 'old']);
  });

  it('places entries without a valid timestamp after timestamped entries', () => {
    const entries = [{ id: 'missing' }, { id: 'invalid', time: 'not-a-time' }, { id: 'valid', time: '2026-08-06T11:00:00Z' }];

    expect(sortLogsNewestFirst(entries).map((entry) => entry.id)).toEqual(['valid', 'missing', 'invalid']);
  });
});
