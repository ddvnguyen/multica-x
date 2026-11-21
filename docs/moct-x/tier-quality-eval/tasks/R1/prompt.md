# Task R1 — tiers list component (React)

Work in the current directory. Write exactly ONE file, `TierList.jsx`. React 18 only (import from `react`), no other dependencies, no CSS files, no tests of your own.

## Contract (exact — a hidden vitest suite renders this component)

```jsx
// default export
export default function TierList({ tiers, onEdit, onRemove }) { ... }
```

- `tiers`: array of `{ id, name, idx, runtimeName, enabled }`, given in arbitrary order.
- Render a `<ul>` with exactly one `<li>` per tier, ordered by `idx` ascending (ties: `name` ascending).
- Every `<li>` shows the tier `name`.
- An enabled tier's `<li>` has two buttons with the accessible names `Edit` and `Remove`; clicking them calls `onEdit(id)` and `onRemove(id)` respectively (exactly once, with the tier id).
- A disabled tier's `<li>` shows the text `unavailable` and its `Edit`/`Remove` buttons are disabled (the `disabled` attribute, not just styling).

Write the file, then stop.
