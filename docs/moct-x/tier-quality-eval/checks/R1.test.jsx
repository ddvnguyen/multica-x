import { test, expect, vi } from 'vitest';
import { render, screen, fireEvent, within } from '@testing-library/react';
import TierList from './TierList.jsx';

const tiers = [
  { id: 't3', name: 'Glacier', idx: 2, runtimeName: 'rt-c', enabled: true },
  { id: 't1', name: 'Fast', idx: 0, runtimeName: 'rt-a', enabled: true },
  { id: 't2', name: 'Cheap', idx: 1, runtimeName: 'rt-b', enabled: false },
];

test('rows are ordered by idx ascending', () => {
  render(<TierList tiers={tiers} onEdit={() => {}} onRemove={() => {}} />);
  const items = screen.getAllByRole('listitem');
  expect(items).toHaveLength(3);
  expect(items[0].textContent).toContain('Fast');
  expect(items[1].textContent).toContain('Cheap');
  expect(items[2].textContent).toContain('Glacier');
});

test('edit and remove report the tier id once', () => {
  const onEdit = vi.fn();
  const onRemove = vi.fn();
  render(<TierList tiers={tiers} onEdit={onEdit} onRemove={onRemove} />);
  const first = screen.getAllByRole('listitem')[0];
  fireEvent.click(within(first).getByRole('button', { name: 'Edit' }));
  fireEvent.click(within(first).getByRole('button', { name: 'Remove' }));
  expect(onEdit).toHaveBeenCalledTimes(1);
  expect(onEdit).toHaveBeenCalledWith('t1');
  expect(onRemove).toHaveBeenCalledWith('t1');
});

test('disabled tier shows unavailable with disabled buttons', () => {
  render(<TierList tiers={tiers} onEdit={() => {}} onRemove={() => {}} />);
  const cheap = screen.getAllByRole('listitem')[1];
  expect(cheap.textContent.toLowerCase()).toContain('unavailable');
  expect(within(cheap).getByRole('button', { name: 'Edit' }).disabled).toBe(true);
  expect(within(cheap).getByRole('button', { name: 'Remove' }).disabled).toBe(true);
});
