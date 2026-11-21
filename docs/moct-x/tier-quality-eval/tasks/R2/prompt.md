# Task R2 — "Use tiers" switch (React)

Work in the current directory. Write exactly ONE file, `UseTiersSwitch.jsx`. React 18 only, no other dependencies, no CSS files, no tests of your own.

## Contract (exact — a hidden vitest suite renders this component)

```jsx
// default export
export default function UseTiersSwitch({ useTiers, tiers, value, onToggle }) { ... }
```

- `tiers`: array of `{ id, name }`.
- A `<button>` with `role="switch"`, `aria-label="Use tiers"` and `aria-checked` reflecting `useTiers`. Clicking it calls `onToggle(!useTiers)` exactly once with a boolean.
- When `useTiers` is **false**: render exactly one `<select>` with the accessible label `model tier`; one `<option>` per tier (`value` = tier id, text = tier name), and the select's value is the `value` prop (a tier id string or `""`). No checkboxes exist.
- When `useTiers` is **true**: no `<select>` is rendered; instead render one `<input type="checkbox">` per tier whose accessible name is the tier name, checked when its id appears in `value` (an array of tier ids). The previously chosen fallback must survive the toggle: a tier that was selected as the single value is checked once `value` becomes `[thatId]`.

The component is controlled: it never keeps selection in local state.

Write the file, then stop.
