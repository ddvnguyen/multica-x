/**
 * Service-worker route rules for the MMX PWA.
 *
 * One contract, two readers: `apps/web/public/sw.js` embeds these lists
 * verbatim (it cannot import TS), and `pwa-cache.test.ts` asserts the
 * embedded copies stay in sync. Edit here first, then copy into sw.js.
 *
 * Policy: cache the app shell and versioned static assets only. API, auth and
 * realtime traffic is live and mutating — it must never be served stale.
 */

/** Path prefixes the service worker must never cache or serve offline. */
export const PWA_NETWORK_ONLY_PREFIXES = ["/api", "/ws", "/auth"] as const;

/** Prefixes served stale-while-revalidate from the versioned asset cache. */
export const PWA_ASSET_PREFIXES = ["/_next/static/", "/icons/", "/icons-mmx/"] as const;

/** Paths precached on install so the shell opens offline. */
export const PWA_PRECACHE_PATHS = ["/offline", "/icons/icon-192.png"] as const;

/** True when the request must go to the network and never touch a cache. */
export function isPwaNetworkOnly(pathname: string): boolean {
  return (PWA_NETWORK_ONLY_PREFIXES as readonly string[]).some(
    (prefix) => pathname === prefix || pathname.startsWith(`${prefix}/`),
  );
}

/** True when the request is a cacheable hashed/static asset. */
export function isPwaAsset(pathname: string): boolean {
  return (PWA_ASSET_PREFIXES as readonly string[]).some((prefix) =>
    pathname.startsWith(prefix),
  );
}
