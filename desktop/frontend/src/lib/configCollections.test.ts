import { describe, expect, it } from 'vitest';
import { joinConfigLines, parseDomainLines, retainConfiguredManualMappings } from './configCollections';

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

describe('retainConfiguredManualMappings', () => {
  it('drops mappings whose manual domain was removed', () => {
    expect(retainConfiguredManualMappings({
      'ani.momoc.top': '104.16.132.229',
      'comfy.momoc.top': '104.19.208.241',
    }, ['ani.momoc.top'])).toEqual({
      'ani.momoc.top': '104.16.132.229',
    });
  });

  it('matches configured domains after case and trailing-dot normalization', () => {
    expect(retainConfiguredManualMappings({
      'ANI.MOMOC.TOP.': '104.16.132.229',
    }, ['ani.momoc.top'])).toEqual({
      'ANI.MOMOC.TOP.': '104.16.132.229',
    });
  });
});
