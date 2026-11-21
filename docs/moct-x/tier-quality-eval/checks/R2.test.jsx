import { test, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import UseTiersSwitch from './UseTiersSwitch.jsx';

const tiers = [
  { id: 't1', name: 'Fast' },
  { id: 't2', name: 'Cheap' },
];

test('off renders exactly one select bound to the fallback value', () => {
  render(<UseTiersSwitch useTiers={false} tiers={tiers} value="t2" onToggle={() => {}} />);
  const select = screen.getByLabelText(/model tier/i);
  expect(select.tagName).toBe('SELECT');
  expect(select.value).toBe('t2');
  expect(screen.getAllByRole('combobox')).toHaveLength(1);
  expect(screen.queryAllByRole('checkbox')).toHaveLength(0);
});

test('switch reports the next state exactly once', () => {
  const onToggle = vi.fn();
  render(<UseTiersSwitch useTiers={false} tiers={tiers} value="t2" onToggle={onToggle} />);
  const sw = screen.getByRole('switch', { name: 'Use tiers' });
  expect(sw.getAttribute('aria-checked')).toBe('false');
  fireEvent.click(sw);
  expect(onToggle).toHaveBeenCalledTimes(1);
  expect(onToggle).toHaveBeenCalledWith(true);
});

test('on renders checkboxes and keeps the fallback selection', () => {
  render(<UseTiersSwitch useTiers tiers={tiers} value={['t2']} onToggle={() => {}} />);
  expect(screen.queryAllByRole('combobox')).toHaveLength(0);
  const boxes = screen.getAllByRole('checkbox');
  expect(boxes).toHaveLength(2);
  expect(boxes[0].checked).toBe(false);
  expect(boxes[1].checked).toBe(true);
});
