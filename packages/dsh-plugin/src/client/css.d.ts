/// <reference types="vite/client" />

/**
 * `island.css?inline` is the one import this package takes that has more than
 * Vite's own `*?inline` shape: the build appends the `@property` rules it
 * extracted from the compiled text (M-148,
 * `scripts/island-property-rules.mjs`). That second export is read off the
 * module namespace in `popup-seat.ts`, because TypeScript resolves the
 * `*?inline` declaration before any more specific pattern, so a narrower
 * declaration here would be shadowed rather than merged.
 */
