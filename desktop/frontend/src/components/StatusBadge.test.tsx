import { MantineProvider } from '@mantine/core';
import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { StatusBadge } from './StatusBadge';
import { verificationTone } from '../lib/status';

describe('StatusBadge', () => {
  it('renders verified state with an accessible label', () => {
    render(<MantineProvider><StatusBadge label="策略已验证" tone="verified" /></MantineProvider>);
    expect(screen.getByText('策略已验证')).toBeVisible();
  });

  it('does not treat missing evidence as verified', () => {
    expect(verificationTone(false)).toBe('neutral');
    expect(verificationTone(undefined)).toBe('neutral');
  });
});
