"use client";

import { Suspense, useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import { TabBar } from "@multica/views/tabs";
import { useNavigationInputBindings } from "@multica/views/tabs";
import { selectTabByShortcut } from "@multica/views/tabs";
import {
  WebTabNavigationProvider,
  WebTabSync,
} from "@/platform/web-tab-navigation";

/**
 * Tabbed shell for one workspace route in the SPA. Mounts the shared tab
 * strip (same store, same strip behavior as desktop) above the page, keeps
 * the store↔router sync alive, and wires the tab keyboard model:
 *
 * - Cmd/Ctrl+1..9 select tabs by position (9 = last);
 * - Cmd/Ctrl+←/→ and mouse side buttons step the active tab's virtual
 *   history (same hook desktop mounts in its shell);
 * - Cmd/Ctrl+[/] goBack/goForward come from <GlobalShortcuts>'s shared
 *   registry, unchanged — they call the adapter, which now reads the session
 *   stack.
 *
 * Tab state persists per browser profile (zustand persist over localStorage,
 * `multica_tabs` key, shared with the store module). Cross-tab races merge
 * per workspace group, so two browser tabs don't erase each other's tabs.
 */
export function WebTabShell({ children }: { children: React.ReactNode }) {
  return (
    <WebTabNavigationProvider>
      <WebTabShellInner>{children}</WebTabShellInner>
    </WebTabNavigationProvider>
  );
}

function WebTabShellInner({ children }: { children: React.ReactNode }) {
  const { data: workspaces = [] } = useQuery(workspaceListOptions());
  const validSlugs = workspaces.map((w) => w.slug);

  useNavigationInputBindings();
  useWebTabKeyboard();

  return (
    <>
      <Suspense fallback={null}>
        <WebTabSync validSlugs={validSlugs} />
      </Suspense>
      {/* Same strip as desktop (shared TabBar, no detached-window host). */}
      <div className="flex h-10 shrink-0 items-stretch border-b border-surface-border bg-app-shell">
        <div className="min-w-0 flex-1">
          <TabBar />
        </div>
      </div>
      {children}
    </>
  );
}

function useWebTabKeyboard(): void {
  // Cmd/Ctrl+1..9: browser-style tab selection. Browsers own these chords
  // for their own tabs, so the page can only claim them when it holds focus
  // and the event reaches the document — the same best-effort deal desktop's
  // main-process accelerators improve on. Reserved in the shared shortcut
  // registry so Settings cannot rebind them into something else.
  // Mount once per shell lifetime; StrictMode double-invoke is harmless
  // (subscribe/unsubscribe round-trips cleanly).
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey;
      if (!mod || e.altKey || e.shiftKey) return;
      if (isEditableShortcutTarget(e.target)) return;
      const digit = e.key >= "1" && e.key <= "9" ? Number(e.key) : null;
      if (digit === null) return;
      e.preventDefault();
      selectTabByShortcut(digit as 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9);
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);
}



function isEditableShortcutTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  const tag = target.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
}
