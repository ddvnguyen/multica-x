import { useSyncExternalStore } from "react";
import { createIslandStorage, type IslandStorage } from "./storage";

/**
 * The panel's zoom, as a device preference.
 *
 * A module store, not a context: the row that writes it lives inside the
 * fork's Preferences tab, which the island does not own, and the surface that
 * reads it sits above the provider tree. One module-level value is what lets
 * both see it without threading a prop through the fork — the same shape
 * `unread-store.ts` uses for the rail dot.
 *
 * Only the panel's own surface scales. Overlay popups are portalled outside it
 * (`popup-seat.ts`), so they keep their own size and stay correctly anchored;
 * that is a deliberate limit, not an oversight — a `zoom` on the seat would
 * displace every `position: fixed` child by `(zoom − 1) × offset`.
 */

export const ZOOM_LEVELS = [80, 90, 100, 110, 125, 150] as const;
export type ZoomLevel = (typeof ZOOM_LEVELS)[number];
export const DEFAULT_ZOOM: ZoomLevel = 100;

/** Storage key under the island's `multica.dsh.` prefix. */
export const ZOOM_STORAGE_KEY = "zoom";

/** The nearest supported level; anything unreadable is the default. */
export function normalizeZoom(value: unknown): ZoomLevel {
  const parsed =
    typeof value === "number" ? value : Number.parseFloat(String(value ?? ""));
  if (!Number.isFinite(parsed)) return DEFAULT_ZOOM;
  let nearest: ZoomLevel = ZOOM_LEVELS[0];
  for (const level of ZOOM_LEVELS) {
    if (Math.abs(level - parsed) < Math.abs(nearest - parsed)) nearest = level;
  }
  return nearest;
}

export function readZoom(storage: IslandStorage): ZoomLevel {
  return normalizeZoom(storage.getItem(ZOOM_STORAGE_KEY));
}

export interface ZoomStore {
  get(): ZoomLevel;
  set(level: unknown): void;
  subscribe(listener: () => void): () => void;
}

export function createZoomStore(storage: IslandStorage): ZoomStore {
  let current = readZoom(storage);
  const listeners = new Set<() => void>();
  return {
    get: () => current,
    set(level) {
      const next = normalizeZoom(level);
      if (next === current) return;
      current = next;
      storage.setItem(ZOOM_STORAGE_KEY, String(next));
      for (const listener of [...listeners]) listener();
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}

/**
 * The one store the panel and its Preferences row share. Both wrap the same
 * `window.localStorage`, so a write through either is visible to the other.
 */
export const zoomStore = createZoomStore(createIslandStorage());

/** The current level, re-rendering the caller when it changes. */
export function useZoom(): ZoomLevel {
  return useSyncExternalStore(
    zoomStore.subscribe,
    zoomStore.get,
    () => DEFAULT_ZOOM,
  );
}
