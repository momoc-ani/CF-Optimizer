import { describe, expect, it } from 'vitest';
import { joinConfigLines, parseDomainLines } from './configCollections';

describe('joinConfigLines', () => {
  it('normalizes nullable migrated collections', () => {
    expect(joinConfigLines(null)).toBe('');
    expect(joinConfigLines(undefined)).toBe('');
    expect(joinConfigLines(['ani.momoc.top', 'cdn.example.com'])).toBe('ani.momoc.top\ncdn.example.com');
  });
});

describe('parseDomainLines', () => {
  it('normalizes, deduplicates and sorts exact domains', () => {
    expect(parseDomainLines(' CDN.Example.com.\nani.momoc.top,cdn.example.com\n')).toEqual([
      'ani.momoc.top',
      'cdn.example.com',
    ]);
  });
});
