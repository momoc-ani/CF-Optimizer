import { describe, expect, it } from 'vitest';
import { shouldApplyPolicy } from './policyAvailability';

describe('shouldApplyPolicy', () => {
  it('allows policy application only when the runtime supports it and the user requested it', () => {
    expect(shouldApplyPolicy(true, true)).toBe(true);
    expect(shouldApplyPolicy(false, true)).toBe(false);
    expect(shouldApplyPolicy(true, false)).toBe(false);
  });
});
