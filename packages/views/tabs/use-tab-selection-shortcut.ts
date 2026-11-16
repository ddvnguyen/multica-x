import { useEffect } from "react";
import { useTabStore } from "@multica/core/tabs";

/** Browser tab-selection keys: 1..8 select by position, 9 selects the last. */
export type TabSelectionShortcutKey = 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9;

/**
 * Select a product tab using browser numbering semantics:
 *
 * - 1..8 select that exact one-based position.
 * - 9 always selects the final tab, even when more than nine tabs exist.
 *
 * Missing positions deliberately no-op. Activation goes through the tab store
 * so the host's router sync updates the visible route while every session
 * keeps its own history and memento.
 */
export function selectTabByShortcut(key: TabSelectionShortcutKey): void {
  const store = useTabStore.getState();
  const slug = store.activeWorkspaceSlug;
  if (!slug) return;

  const group = store.byWorkspace[slug];
  if (!group) return;

  const tab = key === 9 ? group.tabs.at(-1) : group.tabs[key - 1];
  if (!tab) return;

  store.setActiveTab(tab.id);
}

export interface TabSelectionShortcutHost {
  /**
   * Desktop only: subscribe to the main-process Cmd/Ctrl+1..9 accelerators.
   * The Electron main process owns these chords (they are reserved app-wide)
   * and routes them to the tabbed window. Web resolves these through the
   * shared shortcut registry instead, so it leaves this undefined.
   */
  subscribeSelectTabShortcut?: (
    listener: (key: TabSelectionShortcutKey) => void,
  ) => (() => void) | undefined;
  /** True when this renderer must not handle selection (detached issue window). */
  isSelectionSuspended?: () => boolean;
}

/**
 * Receive fixed Cmd/Ctrl+1..9 accelerators from the host.
 * Only the tabbed main surface subscribes.
 */
export function useTabSelectionShortcut(host?: TabSelectionShortcutHost): void {
  useEffect(() => {
    if (!host?.subscribeSelectTabShortcut) return undefined;
    if (host.isSelectionSuspended?.()) return undefined;
    // Optional call keeps renderer HMR safe while an old preload remains
    // attached to a refreshed React tree.
    return host.subscribeSelectTabShortcut(selectTabByShortcut);
  }, [host]);
}
