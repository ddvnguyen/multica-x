"use client";

import { useDefaultLayout } from "react-resizable-panels";
import { deviceStorageOverride } from "../platform/device-storage";

/**
 * A saved split layout: `react-resizable-panels`' `useDefaultLayout`, pointed
 * at the fork's device storage.
 *
 * The library defaults to `localStorage`, which is right on web and desktop and
 * wrong inside the dsh island: the island shares dsh's origin, so a bare
 * `react-resizable-panels:<id>` key would be visible to the shell and to any
 * later Multica page there. Every group that remembers its sizes goes through
 * this hook instead of calling the library directly, so the host decides where
 * the sizes live (M-336, phase 2).
 *
 * The storage object must be stable across renders; a fresh one each render
 * re-reads the saved layout on every commit.
 */
export function useSavedLayout(id: string, panelIds?: string[]) {
  return useDefaultLayout({
    id,
    ...(panelIds === undefined ? {} : { panelIds }),
    // No host adapter means no `storage` argument at all: the library keeps its
    // own `localStorage` default, which is the web and desktop path unchanged.
    storage: deviceStorageOverride(),
  });
}
