import type { MetadataRoute } from "next";
import { pwaBrand } from "@/lib/pwa-brand";

/**
 * Installable web manifest. Start at /inbox so session-aware proxy routing
 * opens the workspace or login; the marketing host's root would open the landing
 * page even for a signed-in user.
 *
 * Portal note (M-404): inside the /p/<port>/ iframe the browser installs from
 * the top-level origin (portal.ddvnguyen.com), whose combined gateway worker
 * owns the "/" scope — the app manifest is install metadata there, not the
 * install itself. Users install from the direct lane URL instead
 * (http://127.0.0.1:3190 on lane z), where scope "/" matches. Asset paths
 * stay root-relative on purpose: the portal gateway rewrites them per
 * request, and the installed app at its own origin needs them unprefixed.
 *
 * Branding comes from `pwaBrand()` (`MMX_PWA=1` selects the MMX installable
 * app: own id/name/icons so it installs beside multica-web). Standard icons
 * share desktop artwork. The maskable SVG supplies full-bleed artwork for
 * launcher cropping; regenerate raster sizes from those source assets.
 */

/** Launch path. Exported so manifest.test.ts can run it through the proxy. */
export const PWA_START_URL = "/inbox";

export default function manifest(): MetadataRoute.Manifest {
  const brand = pwaBrand();
  const dir = brand.iconDir;
  return {
    id: brand.id,
    name: brand.name,
    short_name: brand.short_name,
    description: brand.description,
    start_url: PWA_START_URL,
    scope: "/",
    display: "standalone",
    // Splash-screen colours. The runtime status bar is driven by the
    // per-scheme `<meta name="theme-color">` pair in app/layout.tsx, which the
    // manifest cannot express — these are the pre-launch fallback only.
    background_color: "#ffffff",
    theme_color: "#ffffff",
    categories: ["productivity"],
    icons: [
      {
        src: `/${dir}/icon-192.png`,
        sizes: "192x192",
        type: "image/png",
        purpose: "any",
      },
      {
        src: `/${dir}/icon-512.png`,
        sizes: "512x512",
        type: "image/png",
        purpose: "any",
      },
      {
        src: `/${dir}/icon-maskable-512.png`,
        sizes: "512x512",
        type: "image/png",
        purpose: "maskable",
      },
    ],
    // Long-press actions on the installed launcher icon. Both are legacy
    // segments, so they resolve to the last workspace the same way start_url
    // does instead of needing a slug the manifest cannot know.
    shortcuts: [
      { name: "Inbox", url: "/inbox" },
      { name: "My Issues", url: "/my-issues" },
    ],
  };
}
