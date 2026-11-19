# Task R4 — queued tier badge (React)

Work in the current directory. Write exactly ONE file, `QueuedBadge.jsx`. React 18 only, no other dependencies, no CSS files, no tests of your own.

## Contract (exact — a hidden vitest suite renders this component)

```jsx
// default export
export default function QueuedBadge({ explicitTier, queued, reason }) { ... }
```

- When `queued` is falsy the component renders nothing (returns `null`).
- When `queued` is truthy it renders a single element with `role="status"` whose text content contains the word `queued` (lowercase) and the `reason` string verbatim, so the user learns *why* the task is waiting.
- When `explicitTier` is a non-empty string, that same element's text also contains `@` followed by the tier name (e.g. `@fast`), naming the tier the user pinned.

Write the file, then stop.
