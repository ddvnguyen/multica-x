"use client";

import type { ReactNode } from "react";
import { SidebarProvider } from "@multica/ui/components/ui/sidebar";
import { WorkspaceTrialNotices } from "../billing/trial-notices";
import { AppearanceSync } from "../common/appearance-sync";
import { ModalRegistry } from "../modals/registry";
import { SourceBackfillModal } from "../onboarding";
import { AppRail } from "./app-rail";
import { AppBottomBar } from "./bottom-bar";
import { AppShellProvider } from "./app-shell-context";
import { CompactNavSheet } from "./compact-nav-sheet";
import { DashboardGuard } from "./dashboard-guard";
import { NavigationProgress } from "./navigation-progress";
import { ShellRouteDataPrefetch } from "./route-data-prefetch";
import { SpacePanel } from "./space-panel";
import { WorkspacePresencePrefetch } from "./workspace-presence-prefetch";
import { GlobalShortcuts } from "./global-shortcuts";

interface DashboardLayoutProps {
  children: ReactNode;
  /** Rendered inside the content column (e.g. ChatWindow, ChatFab — absolute-positioned overlays) */
  extra?: ReactNode;
  /** Loading indicator */
  loadingIndicator?: ReactNode;
  /**
   * A strip above the content card, spanning the section panel and the page
   * (M-413). The web SPA puts its tab strip here so it covers the section menu
   * instead of starting at the card's leading edge; desktop builds its own
   * layout, so nothing renders here unless a host passes it.
   */
  header?: ReactNode;
}

/**
 * The content card: the space panel and the page share one surface on the
 * app shell, split by a single hairline. From `md` up the card is inset by a
 * gutter on every side, with the bottom bar below it; below `md` the card is
 * the whole window. The bottom gutter stays whether or not the bar renders —
 * the web shell can drop the bar entirely (M-413), and a card flush against
 * the window edge lost its ring and its rounded bottom corners. Desktop
 * builds the same card in its own layout, under the tab bar.
 */
export function AppShellCard({ children }: { children: ReactNode }) {
  return (
    <main data-mc="page" className="relative flex min-h-0 min-w-0 flex-1 flex-row overflow-hidden bg-page-canvas md:my-2 md:mr-2 md:rounded-page md:shadow-[var(--page-shadow,var(--surface-shadow))] md:ring-1 md:ring-page-border">
      <SpacePanel />
      <div className="relative flex min-w-0 flex-1 flex-col overflow-hidden">{children}</div>
    </main>
  );
}

export function DashboardLayout({
  children,
  extra,
  loadingIndicator,
  header,
}: DashboardLayoutProps) {
  return (
    <>
      <AppearanceSync />
      <DashboardGuard
        loadingFallback={
          <div className="flex h-svh items-center justify-center">
            {loadingIndicator}
          </div>
        }
      >
        {/* Fills the screen with its content inside the safe area, so the
            strips match what borders them: the card below `md`, where it is
            the whole window, the app shell from `md` up. `data-app-shell`
            lifts the body's side insets (apps/web/app/globals.css). */}
        <SidebarProvider
          data-app-shell=""
          className="h-svh bg-page-canvas pt-[env(safe-area-inset-top)] pr-[env(safe-area-inset-right)] pb-[env(safe-area-inset-bottom)] pl-[env(safe-area-inset-left)] [--sidebar-wrapper-fill:var(--page-canvas)] md:bg-app-shell md:[--sidebar-wrapper-fill:var(--app-shell)]"
        >
          <AppShellProvider>
            <ShellRouteDataPrefetch>
              <GlobalShortcuts />
              <WorkspacePresencePrefetch />
              <AppRail className="hidden md:flex" />
              <CompactNavSheet />
              <div className="flex min-w-0 flex-1 flex-col">
                {header}
                <AppShellCard>
                  <NavigationProgress />
                  <WorkspaceTrialNotices />
                  {children}
                  <ModalRegistry />
                  <SourceBackfillModal />
                  {extra}
                </AppShellCard>
                <AppBottomBar />
              </div>
            </ShellRouteDataPrefetch>
          </AppShellProvider>
        </SidebarProvider>
      </DashboardGuard>
    </>
  );
}
