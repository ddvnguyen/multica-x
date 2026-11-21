import { test, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import QueuedBadge from './QueuedBadge.jsx';

test('renders nothing when not queued', () => {
  const { container } = render(<QueuedBadge explicitTier="fast" queued={false} reason="full" />);
  expect(container.textContent).toBe('');
  expect(screen.queryByRole('status')).toBeNull();
});

test('shows the queued reason and the pinned tier', () => {
  render(<QueuedBadge explicitTier="fast" queued reason="tier fast is full" />);
  const status = screen.getByRole('status');
  expect(status.textContent.toLowerCase()).toContain('queued');
  expect(status.textContent).toContain('tier fast is full');
  expect(status.textContent).toContain('@fast');
});
