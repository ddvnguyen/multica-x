/** Type surface for mmx-version.mjs — the same pattern as island-property-rules.d.mts. */

/** What one build knows about itself: the stamp plus tooltip material. */
export interface MmxBuild {
  /** The multica version of the fork, from apps/web/package.json. */
  version: string;
  /** Full string: `x.x.x-yyMMMdd.HH.mm` on the UTC+7 clock. */
  stamp: string;
  /** The stamp's instant in UTC, minute precision: `2026-10-28T14:58Z`. */
  utc: string;
  /** Short HEAD sha, `unknown` outside a git checkout. */
  sha: string;
}

/** The fork's multica version — single source, apps/web/package.json. */
export function readVersion(root?: string): string;

/** The suffix for an instant, read on the UTC+7 clock: `26OCT28.21.58`. */
export function stampFor(date: Date): string;

/** Tooltip line for the same instant, in UTC: `2026-10-28T14:58Z`. */
export function utcLabel(date: Date): string;

/** HEAD's committer instant — what makes one SHA produce one string. */
export function committerTime(): Date;

/**
 * The full payload. `fromGit` pins the stamp to HEAD's committer time and
 * refuses to continue without git; the wall-clock path degrades to an
 * "unknown" sha so a plain local `vite build` still works outside a repo.
 */
export function collect(options?: {
  fromGit?: boolean;
  now?: Date;
  sha?: string | null;
}): MmxBuild;

/**
 * The dsh-z multica-be release.json line, carrying "version" beside
 * sha/ref/builtAt. The stamp always comes from HEAD's committer time.
 */
export function releaseJson(options: {
  ref: string;
  sha: string;
  builtAt?: Date;
}): string;
