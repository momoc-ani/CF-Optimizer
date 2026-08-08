import { describe, expect, it } from 'vitest';
import { canApplyManualDomain, shouldApplyPolicy } from './policyAvailability';

describe('shouldApplyPolicy', () => {
  it('allows policy application only when the runtime supports it and the user requested it', () => {
    expect(shouldApplyPolicy(true, true)).toBe(true);
    expect(shouldApplyPolicy(false, true)).toBe(false);
    expect(shouldApplyPolicy(true, false)).toBe(false);
  });
});

describe('canApplyManualDomain', () => {
  it('requires a passing test for the current draft address', () => {
    expect(canApplyManualDomain(true, '104.16.0.1', '104.16.0.1')).toBe(true);
    expect(canApplyManualDomain(false, '104.16.0.1', '104.16.0.1')).toBe(false);
    expect(canApplyManualDomain(true, '104.16.0.1', '104.16.0.2')).toBe(false);
    expect(canApplyManualDomain(true, undefined, '104.16.0.1')).toBe(false);
  });
});
