/**
 * MMX PWA brand switch.
 *
 * One env var, read at build time: `MMX_PWA=1` selects the MMX branding for
 * the installable manifest (and the matching icon set / apple title). The
 * default build is unchanged multica-web branding. Build-time, not runtime:
 * the manifest is generated during `next build`, so a runtime flag could not
 * change what the browser installed.
 *
 * Pure and React-free so manifest.ts (a server module) and the vitest suite
 * share it without pulling in components. Field names match
 * `MetadataRoute.Manifest` (`short_name`, not camelCase).
 */

/** True when this build is the MMX PWA (second installable app). */
export function isMmxPwa(env: Record<string, string | undefined> = process.env): boolean {
  return env.MMX_PWA === "1";
}

export interface PwaBrand {
  /** Manifest `id`: suffixed so the PWA installs beside multica-web. */
  id: string;
  name: string;
  short_name: string;
  description: string;
  /** Icon directory under `public/`, without leading/trailing slash. */
  iconDir: string;
  /** iOS home-screen title (`appleWebApp.title` in layout). */
  appleTitle: string;
}

const MULTICA_BRAND: PwaBrand = {
  id: "/",
  name: "Multica",
  short_name: "Multica",
  description:
    "Assign tasks to coding agents, track progress, and keep your team's work in one place.",
  iconDir: "icons",
  appleTitle: "Multica",
};

const MMX_BRAND: PwaBrand = {
  id: "/?mmx-install",
  name: "MMX",
  short_name: "MMX",
  description:
    "MMX by Multica: assign tasks to coding agents, track progress, and keep your team's work in one place.",
  iconDir: "icons-mmx",
  appleTitle: "MMX",
};

/** Brand for this build. Single place both manifest.ts and layout read. */
export function pwaBrand(env: Record<string, string | undefined> = process.env): PwaBrand {
  return isMmxPwa(env) ? MMX_BRAND : MULTICA_BRAND;
}
