"use client";

import { useEffect, useState } from "react";

/**
 * PWA update banner. Registers `/sw.js` (versioned per build) and prompts
 * when a newer service worker is waiting, instead of silently leaving the
 * user on a stale shell after a deploy.
 *
 * Renders nothing when service workers are unavailable, registration fails,
 * or no update is waiting. The copy is inline (not i18n): this is
 * install/update chrome, shown before the app shell loads its bundles.
 */
export function PwaUpdateBanner() {
  const [waiting, setWaiting] = useState<ServiceWorker | null>(null);

  useEffect(() => {
    if (!("serviceWorker" in navigator)) return;
    // Inside the portal iframe the gateway owns /sw.js (scope "/") and the
    // app's own worker could never control the page — registering would
    // throw a scope error on every load. The gateway's worker (already
    // active at the top level) keeps the offline/update story there.
    try {
      if (window.self !== window.top) return;
    } catch {
      return;
    }
    let cancelled = false;

    const track = (reg: ServiceWorkerRegistration) => {
      if (reg.waiting && !cancelled) setWaiting(reg.waiting);
      reg.addEventListener("updatefound", () => {
        const next = reg.installing;
        if (!next) return;
        next.addEventListener("statechange", () => {
          if (next.state === "installed" && reg.waiting && !cancelled) {
            setWaiting(reg.waiting);
          }
        });
      });
    };

    navigator.serviceWorker
      .register("/sw.js")
      .then(track)
      .catch(() => {
        // Offline-first boot or a gateway that strips the worker: the app
        // still works, it just has no update channel on this load.
      });

    const onControllerChange = () => window.location.reload();
    navigator.serviceWorker.addEventListener("controllerchange", onControllerChange);
    return () => {
      cancelled = true;
      navigator.serviceWorker.removeEventListener("controllerchange", onControllerChange);
    };
  }, []);

  if (!waiting) return null;

  return (
    <div
      role="status"
      className="fixed inset-x-0 top-0 z-[100] flex items-center justify-center gap-3 bg-foreground px-4 py-2 text-sm text-background"
    >
      <span>Update available — refresh for the latest version.</span>
      <button
        type="button"
        className="rounded-md px-2 py-0.5 font-medium underline underline-offset-2"
        onClick={() => {
          waiting.postMessage("SKIP_WAITING");
        }}
      >
        Refresh
      </button>
    </div>
  );
}
