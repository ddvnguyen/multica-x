/* MMX PWA service worker (hand-written, no library).
 *
 * Policy (contract: apps/web/lib/pwa-cache.ts — edit there first, copy here):
 * cache the app shell + versioned static assets only; NEVER cache /api, /ws
 * or /auth. Navigations are network-first with the cached /offline page as
 * fallback. Versioned with the build: installing a new SW version precaches
 * fresh and deletes every older mmx-* cache on activate.
 *
 * The VERSION const below is baked at build time: scripts/build-mmx-pwa.sh
 * replaces the __MMX_PWA_VERSION__ placeholder with
 * NEXT_PUBLIC_APP_VERSION (git sha in CI, "dev" locally). A literal keeps
 * this file dependency-free: no build step may import it.
 */

const VERSION = "__MMX_PWA_VERSION__";
const SHELL_CACHE = `mmx-shell-${VERSION}`;
const ASSET_CACHE = `mmx-assets-${VERSION}`;

// Mirrors PWA_PRECACHE_PATHS in apps/web/lib/pwa-cache.ts.
const PRECACHE_PATHS = ["/offline", "/icons/icon-192.png"];

// Mirrors PWA_NETWORK_ONLY_PREFIXES in apps/web/lib/pwa-cache.ts.
const NETWORK_ONLY = ["/api", "/ws", "/auth"];

// Mirrors PWA_ASSET_PREFIXES in apps/web/lib/pwa-cache.ts.
const ASSET_PREFIXES = ["/_next/static/", "/icons/", "/icons-mmx/"];

function isNetworkOnly(pathname) {
  return NETWORK_ONLY.some((p) => pathname === p || pathname.startsWith(p + "/"));
}

function isAsset(pathname) {
  return ASSET_PREFIXES.some((p) => pathname.startsWith(p));
}

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches
      .open(SHELL_CACHE)
      .then((cache) => cache.addAll(PRECACHE_PATHS))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(
          keys
            .filter((k) => k.startsWith("mmx-") && k !== SHELL_CACHE && k !== ASSET_CACHE)
            .map((k) => caches.delete(k)),
        ),
      )
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("message", (event) => {
  if (event.data === "SKIP_WAITING") self.skipWaiting();
});

self.addEventListener("fetch", (event) => {
  const url = new URL(event.request.url);
  if (url.origin !== self.location.origin) return;
  if (event.request.method !== "GET") return;

  // Live traffic: network only, never cached, never served stale.
  if (isNetworkOnly(url.pathname)) return;

  // Versioned assets: cache-first, refresh in background.
  if (isAsset(url.pathname)) {
    event.respondWith(
      caches.match(event.request).then(
        (hit) =>
          hit ||
          fetch(event.request).then((res) => {
            const copy = res.clone();
            caches.open(ASSET_CACHE).then((cache) => cache.put(event.request, copy));
            return res;
          }),
      ),
    );
    return;
  }

  // Navigations + everything else: network first, offline page as fallback.
  event.respondWith(
    fetch(event.request)
      .then((res) => {
        if (event.request.mode === "navigate" && res.ok) {
          const copy = res.clone();
          caches.open(SHELL_CACHE).then((cache) => cache.put(event.request, copy));
        }
        return res;
      })
      .catch(() => {
        if (event.request.mode === "navigate") {
          return caches.match("/offline").then((page) => {
            if (page) return page;
            return caches.match(event.request);
          });
        }
        return caches.match(event.request);
      }),
  );
});
