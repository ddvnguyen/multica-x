"use client";

import { Suspense, useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import {
  ChatTabsInHeaderProvider,
  TabBar,
  TabHistoryButtons,
} from "@multica/views/tabs";
import { useNavigationInputBindings } from "@multica/views/tabs";
import { selectTabByShortcut } from "@multica/views/tabs";
import {
  WebTabNavigationProvider,
  WebTabSync,
} from "@/platform/web-tab-navigation";

/**
 * Tabbed shell for the workspace routes in the SPA. The pieces are split by
 * where they mount, because the strip and the page are no longer siblings
 * (M-413 round 2):
 *
 * - `WebTabProvider` owns the navigation adapter and the chat placement for
 *   the whole dashboard, including chrome the tab strip does not contain;
 * - `WebTabStrip` is the header slot the dashboard renders above the content
 *   card, so the strip spans the section side menu as well as the page;
 * - `WebTabShell` keeps the store↔router sync and the keyboard model alive
 *   around the page.
 *
 * Same store, same strip behavior as desktop:
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
export function WebTabProvider({ children }: { children: React.ReactNode }) {
  return (
    <WebTabNavigationProvider>
      {/* Open chats are tabs of the header strip here, not chips under the
          page (M-413): one strip for "where am I". The bottom bar reads the
          same provider, drops its chips and its Ask Multica, and leaves the
          footer empty. */}
      <ChatTabsInHeaderProvider>{children}</ChatTabsInHeaderProvider>
    </WebTabNavigationProvider>
  );
}

/**
 * The strip. Lives in the dashboard header slot, above the content card and
 * the section panel, so it reads as the window's chrome rather than the
 * page's toolbar: Back and Forward at the leading edge, then the tabs — the
 * page tabs, a hairline, and the chats.
 */
export function WebTabStrip() {
  return (
    <div
      data-slot="web-tab-strip"
      className="flex h-7 shrink-0 items-stretch gap-0.5 border-b border-surface-border bg-app-shell pr-2"
    >
      <TabHistoryButtons />
      <div className="min-w-0 flex-1">
        <TabBar compact />
      </div>
    </div>
  );
}

export function WebTabShell({ children }: { children: React.ReactNode }) {
  return <WebTabShellInner>{children}</WebTabShellInner>;
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
