/**
 * The stamped identity of the build a host is running (M-155).
 *
 * One string identifies one build — `x.x.x-yyMMMdd.HH.mm`, produced by
 * `scripts/mmx-version.mjs` and injected into the plugin bundle by
 * `packages/dsh-plugin/vite.config.ts` as `__MMX_BUILD__` (stamp, the UTC
 * time and short sha behind the badge tooltip, plus the base version).
 *
 * Hosts that do not define the identifier — the web app, the desktop app,
 * ui-lab, plain vitest — get `null` and render no version UI at all. A row
 * showing a made-up value would be worse than no row: the whole point of the
 * string is that it tells the truth about which build produced an error.
 */

/** What one build knows about itself. */
export interface MmxBuild {
  /** The multica version of the fork, from apps/web/package.json. */
  version: string;
  /** Full string: `x.x.x-yyMMMdd.HH.mm` on the UTC+7 clock. */
  stamp: string;
  /** The stamp's instant in UTC, minute precision: `2026-10-28T14:58Z`. */
  utc: string;
  /** Short HEAD sha at build time. */
  sha: string;
}

declare const __MMX_BUILD__: MmxBuild | undefined;

/** The injected build, or null when the host stamped nothing. */
export function mmxBuild(): MmxBuild | null {
  // `typeof` is the only safe probe for an undeclared global: it never
  // throws, so the guarded reference below is only evaluated when defined.
  if (typeof __MMX_BUILD__ !== "object" || __MMX_BUILD__ === null) {
    return null;
  }
  return __MMX_BUILD__;
}
