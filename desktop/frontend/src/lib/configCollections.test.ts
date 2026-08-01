import { describe, expect, it } from 'vitest';
import { joinConfigLines } from './configCollections';

describe('joinConfigLines', () => {
  it('normalizes nullable migrated collections', () => {
    expect(joinConfigLines(null)).toBe('');
    expect(joinConfigLines(undefined)).toBe('');
    expect(joinConfigLines(['ani.momoc.top', 'cdn.example.com'])).toBe('ani.momoc.top\ncdn.example.com');
  });
});
