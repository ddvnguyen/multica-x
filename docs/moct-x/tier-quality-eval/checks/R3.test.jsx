import { test, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import TierPopup, { shouldOpen } from './TierPopup.jsx';

const tiers = [
  { id: 't3', name: 'Flash', idx: 2, enabled: true },
  { id: 't1', name: 'Fast', idx: 0, enabled: true },
  { id: 't2', name: 'Cheap', idx: 1, enabled: false },
];

test('shouldOpen only for a typed hash at a token start', () => {
  expect(shouldOpen('', '#')).toBe(true);
  expect(shouldOpen('hi ', 'hi #')).toBe(true);
  expect(shouldOpen('', '#abc')).toBe(false);
  expect(shouldOpen('x', 'x#')).toBe(false);
  expect(shouldOpen('abc', 'abcd')).toBe(false);
  expect(shouldOpen('hi @agent ', 'hi @agent #')).toBe(true);
});

test('lists enabled tiers in idx order and never the disabled one', () => {
  render(<TierPopup tiers={tiers} query="" onPick={() => {}} />);
  const opts = screen.getAllByRole('option').map((o) => o.textContent);
  expect(opts).toEqual(['Fast', 'Flash']);
});

test('filters by query and reports the picked id', () => {
  const onPick = vi.fn();
  render(<TierPopup tiers={tiers} query="fl" onPick={onPick} />);
  const opts = screen.getAllByRole('option');
  expect(opts).toHaveLength(1);
  fireEvent.click(opts[0]);
  expect(onPick).toHaveBeenCalledTimes(1);
  expect(onPick).toHaveBeenCalledWith('t3');
});
