# Task R3 — `#` tier popup (React)

Work in the current directory. Write exactly ONE file, `TierPopup.jsx`. React 18 only, no other dependencies, no CSS files, no tests of your own.

## Contract (exact — a hidden vitest suite imports both exports)

```jsx
// default export: the popup list
export default function TierPopup({ tiers, query, onPick }) { ... }
// named export: typed-only trigger guard
export function shouldOpen(prevText, nextText) { ... }
```

`tiers`: array of `{ id, name, idx, enabled }`.

- `TierPopup` renders an element with `role="listbox"` containing one `role="option"` per **enabled** tier whose `name` contains `query` case-insensitively, ordered by `idx` ascending. Each option's accessible name is the tier name. Clicking an option calls `onPick(tier.id)` exactly once. Disabled tiers are never listed, even when they match the query.
- `shouldOpen(prevText, nextText)` answers "should the `#` popup open for this editor change?" — `true` only when `nextText` is `prevText` with exactly one character `#` inserted, and the character before that insertion is absent (start of text) or whitespace. A paste that inserts a longer string containing `#`, a `#` appended to a word, or any other change returns `false`.

Write the file, then stop.
