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
  it('requires an available adapter and verified policy', () => {
    expect(canApplyManualDomain(true, true)).toBe(true);
    expect(canApplyManualDomain(false, true)).toBe(false);
    expect(canApplyManualDomain(true, false)).toBe(false);
  });
});
