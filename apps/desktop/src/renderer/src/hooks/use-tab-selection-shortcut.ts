import type { TabSelectionShortcutKey } from "../../../shared/main-renderer-messages";
import { useWindowOverlayStore } from "@/stores/window-overlay-store";
import {
  useTabSelectionShortcut as useSharedTabSelectionShortcut,
  selectTabByShortcut as selectSharedTabByShortcut,
} from "@multica/views/tabs";

export type { TabSelectionShortcutKey };

/** Desktop selection: skip while a window overlay owns the screen. */
export function selectTabByShortcut(key: TabSelectionShortcutKey): void {
  if (useWindowOverlayStore.getState().overlay) return;
  selectSharedTabByShortcut(key);
}

/**
 * Receive fixed Cmd/Ctrl+1..9 accelerators from Electron's main process.
 * Only the tabbed main window subscribes because main routes the shortcut
 * there regardless of which window or renderer control currently has focus.
 */
export function useTabSelectionShortcut(): void {
  useSharedTabSelectionShortcut({
    subscribeSelectTabShortcut: (listener) =>
      window.desktopAPI.onSelectTabShortcut?.(listener),
    isSelectionSuspended: () =>
      window.desktopAPI.windowContext?.kind === "issue",
  });
}
