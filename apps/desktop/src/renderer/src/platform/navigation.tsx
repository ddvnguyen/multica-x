import { useMemo } from "react";
import type { LinkClickIntent } from "@multica/views/navigation";
import { DesktopTabNavigationProvider } from "@multica/views/tabs";
import { useAuthStore } from "@multica/core/auth";
import { workspaceSlugForPath, paths } from "@multica/core/paths";
import { useWindowOverlayStore } from "@/stores/window-overlay-store";
import {
  handOffToWorkspaceWindow,
  openWorkspaceInNewWindow,
  otherWindowWorkspaces,
} from "./workspace-windows";
import { parseIssueWindowPath } from "../../../shared/issue-window";
import {
  routeDesktopContentLinkPath as routeSharedContentLinkPath,
  routeIssueDeepLinkPath as routeSharedIssueDeepLinkPath,
} from "@multica/views/tabs";

function requireRuntimeAppUrl(scope: string): string {
  const runtimeConfig = window.desktopAPI.runtimeConfig;
  if (!runtimeConfig.ok) {
    throw new Error(
      `Invariant violated: ${scope} rendered before App accepted runtime config`,
    );
  }
  return runtimeConfig.config.appUrl;
}

/**
 * Intercept navigation to "transition" paths — pre-workspace flows that on
 * desktop are rendered as a window-level overlay instead of a tab route.
 * Returns `true` if the navigation was handled (caller should NOT proceed).
 *
 * MUL-4741 note: the old adapter also parked the tab's router at "/" when
 * opening these overlays. Under the session architecture the Coordinator
 * parks the single router automatically whenever `activeWorkspaceSlug` goes
 * null (the zero-workspace flows), and an overlay opened over a still-valid
 * workspace simply covers the mounted tab — no navigation happens at all.
 */
function tryRouteToOverlay(path: string): boolean {
  const overlay = useWindowOverlayStore.getState();
  if (path === "/workspaces/new") {
    overlay.open({ type: "new-workspace" });
    return true;
  }
  if (path === "/onboarding") {
    overlay.open({ type: "onboarding" });
    return true;
  }
  if (path === "/invitations") {
    overlay.open({ type: "invitations" });
    return true;
  }
  if (path.startsWith("/invite/") && !workspaceSlugForPath(path)) {
    let id = "";
    try {
      id = decodeURIComponent(path.slice("/invite/".length));
    } catch {
      return true;
    }
    if (id) {
      overlay.open({ type: "invitations", invitationId: id });
      return true;
    }
  }
  // Any other navigation cancels a live overlay.
  if (overlay.overlay) overlay.close();
  return false;
}

/**
 * Content links (`multica:navigate` from shared content) use the shared tab
 * router with the desktop host (multi-window handoff). Kept as a named local
 * so existing call sites don't churn.
 */
export function routeContentLinkPath(
  path: string,
  disposition: LinkClickIntent = "push",
): void {
  routeSharedContentLinkPath(path, disposition, {
    handOffToWorkspaceWindow: (slug: string, openPath?: string) =>
      handOffToWorkspaceWindow(slug, openPath),
  });
}

/**
 * Desktop deep links hand off to the window already showing the workspace
 * when there is one; otherwise they switch workspace (or foreground-open in
 * place) and apply the address through the session history flow.
 */
export function routeIssueDeepLinkPath(path: string): void {
  routeSharedIssueDeepLinkPath(path, {
    handOffToWorkspaceWindow: (slug: string, openPath?: string) =>
      handOffToWorkspaceWindow(slug, openPath, { navigate: true }),
  });
}

/**
 * Navigation provider for the whole desktop shell — sidebar, search dialog,
 * modals, WindowOverlay contents, AND the page tree inside the single
 * RouterProvider (there is no per-tab provider anymore; the active session's
 * URL is the location for everyone).
 *
 * Adapter behavior lives in `@multica/views/tabs` (DesktopTabNavigationProvider);
 * this module supplies the desktop host: overlay intercepts, multi-window
 * handoff, detached issue windows, and the runtime app URL. The overlay,
 * same-pathname-in-pinned-tab (D2a/D2b), workspace-resume, and deep-link
 * behaviors below are desktop-specific deltas the shared adapter delegates
 * to through `onTransitionPath`, `handOffToWorkspaceWindow`, and the
 * `/login` logout hook.
 */
export function DesktopNavigationProvider({
  children,
}: {
  children: React.ReactNode;
}) {
  const host = useMemo(
    () => ({
      getAppUrl: () => requireRuntimeAppUrl("DesktopNavigationProvider"),
      onTransitionPath: tryRouteToOverlay,
      handOffToWorkspaceWindow: (
        slug: string,
        path?: string,
        opts?: { navigate?: boolean },
      ) => handOffToWorkspaceWindow(slug, path, opts),
      workspaceResumePath: (slug: string) => paths.workspace(slug).issues(),
      openWorkspaceInNewWindow,
      workspacesInOtherWindows: () => [...otherWindowWorkspaces()],
      openIssueInNewWindow: (path: string, title: string) => {
        void window.desktopAPI.openIssueWindow({ path, title });
      },
      parseIssueWindowPath: (path: string) => parseIssueWindowPath(path),
      logout: () => {
        useAuthStore.getState().logout();
      },
    }),
    [],
  );

  return (
    <DesktopTabNavigationProvider host={host}>
      {children}
    </DesktopTabNavigationProvider>
  );
}
