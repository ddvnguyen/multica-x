"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, type ReactNode } from "react";
import { useIssueOpeningStore, type IssueOpenMode } from "@multica/core/issues/stores/issue-opening-store";
import { useWorkspacePaths } from "@multica/core/paths";
import { useNavigation } from "../../navigation";

export type SidePreviewOpenTarget =
  | string
  | {
      view: "issue" | "agent" | "runtime" | "skill" | "project";
      id: string;
    };

interface IssueOpeningCapability {
  supportsTabs: boolean;
  supportsPeek: boolean;
  /**
   * Whether the host opens an issue in place, as a full page route change.
   *
   * The tabbed island has no in-place page: there the full-page experience is
   * a new header tab (M-313), and only that host hides Full page. The web
   * dashboard shows both — a page route inside the tab and a new tab beside it
   * (M-480) — so it keeps every mode on offer.
   */
  supportsPage: boolean;
  openMode: IssueOpenMode;
  openIssueInTab: (issueId: string, activate?: boolean) => boolean;
  /**
   * Where a Side preview click lands when the host owns the column (MMX, M-336).
   *
   * `null` on every host whose side preview is the fork's own in-view peek: web
   * and desktop mount `IssuePeekHost` and need nothing from the platform. A
   * host that draws the preview in its own side column supplies an opener and
   * `IssuePeekHost` stands down, so the two previews never both exist.
   *
   * Accepts an issue id for compatibility, or a `{ view, id }` target for
   * other side-previewable views (G6). Returns whether the press was handled;
   * a host that cannot open the column right now answers `false`.
   */
  openSidePreview: ((target: SidePreviewOpenTarget) => boolean) | null;
}

const DEFAULT_CAPABILITY: IssueOpeningCapability = {
  supportsTabs: false,
  supportsPeek: true,
  supportsPage: true,
  openMode: "page",
  openIssueInTab: () => false,
  openSidePreview: null,
};

const IssueOpeningCapabilityContext = createContext(DEFAULT_CAPABILITY);

/**
 * The mode a click uses: the stored choice, narrowed to what the host can show.
 *
 * A host with an in-place page keeps whatever is stored, including the default
 * Full page for anyone who never chose (M-480). A tab-only host has no page to
 * fall back to, so a legacy Full page means New tab there (M-313) and the
 * default is a tab.
 */
function resolveOpenMode(
  mode: IssueOpenMode,
  hasUserChoice: boolean,
  supportsTabs: boolean,
  supportsPeek: boolean,
  supportsPage: boolean,
): IssueOpenMode {
  const inPlace: IssueOpenMode = supportsPage ? "page" : "tab";
  if (!hasUserChoice) return inPlace;
  if (mode === "tab") return supportsTabs ? "tab" : inPlace;
  if (mode === "peek") return supportsPeek ? "peek" : inPlace;
  return inPlace;
}

/** Platform-owned issue opening behavior: which modes this host offers. */
export function IssueOpeningCapabilityProvider({
  children,
  supportsTabs = false,
  supportsPeek = true,
  supportsPage = !supportsTabs,
  openSidePreview = null,
}: {
  children: ReactNode;
  supportsTabs?: boolean;
  supportsPeek?: boolean;
  /**
   * Defaults to `!supportsTabs`: a host that never declared tabs keeps its
   * in-place full page, and the tabbed island keeps treating a full page as
   * a new tab. A host with both — the web dashboard (M-480) — opts in.
   */
  supportsPage?: boolean;
  /** A host-owned side column; see `IssueOpeningCapability.openSidePreview`. */
  openSidePreview?: ((target: SidePreviewOpenTarget) => boolean) | null;
}) {
  const mode = useIssueOpeningStore((state) => state.openMode);
  const hasUserChoice = useIssueOpeningStore((state) => state.hasUserChoice);
  const setOpenMode = useIssueOpeningStore((state) => state.setOpenMode);
  const setDefaultOpenMode = useIssueOpeningStore((state) => state.setDefaultOpenMode);
  const navigation = useNavigation();
  const paths = useWorkspacePaths();

  useEffect(() => {
    // Only a host with no in-place page migrates. A host that keeps the page
    // (web, desktop) must leave the stored choice exactly as it is: rewriting
    // it would move every existing user off Full page (M-480).
    if (!supportsTabs || supportsPage) return;
    // The tabbed island (MMX) has no in-place full page: the full-page
    // experience is a new header tab (M-313), so a stored "Full page" becomes
    // "New tab". A stored peek choice stays one — the island always has a
    // preview to offer, dsh's right column where the shell publishes the tab
    // seam and the fork's floating peek where it does not (M-336).
    if (supportsPeek) {
      if (mode === "page") setOpenMode("tab");
      else if (!hasUserChoice && mode !== "tab") setDefaultOpenMode("tab");
      return;
    }
    if (mode === "peek") setOpenMode("tab");
    else if (!hasUserChoice) setDefaultOpenMode("tab");
  }, [supportsTabs, supportsPage, supportsPeek, mode, hasUserChoice, setOpenMode, setDefaultOpenMode]);

  const openIssueInTab = useCallback((issueId: string, activate = true) => {
    if (!supportsTabs || !navigation.openInNewTab) return false;
    navigation.openInNewTab(paths.issueDetail(issueId), issueId, { activate });
    return true;
  }, [supportsTabs, navigation, paths]);

  const value = useMemo<IssueOpeningCapability>(() => ({
    supportsTabs,
    supportsPeek,
    supportsPage,
    openSidePreview,
    openMode: resolveOpenMode(mode, hasUserChoice, supportsTabs, supportsPeek, supportsPage),
    openIssueInTab,
  }), [supportsTabs, supportsPeek, supportsPage, openSidePreview, hasUserChoice, mode, openIssueInTab]);

  return (
    <IssueOpeningCapabilityContext.Provider value={value}>
      {children}
    </IssueOpeningCapabilityContext.Provider>
  );
}

export function useIssueOpeningCapability() {
  return useContext(IssueOpeningCapabilityContext);
}
