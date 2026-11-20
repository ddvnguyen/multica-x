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
  openMode: "page",
  openIssueInTab: () => false,
  openSidePreview: null,
};

const IssueOpeningCapabilityContext = createContext(DEFAULT_CAPABILITY);

/** Platform-owned issue opening behavior. Web and desktop keep the upstream modes. */
export function IssueOpeningCapabilityProvider({
  children,
  supportsTabs = false,
  supportsPeek = true,
  openSidePreview = null,
}: {
  children: ReactNode;
  supportsTabs?: boolean;
  supportsPeek?: boolean;
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
    if (!supportsTabs) return;
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
  }, [supportsTabs, supportsPeek, mode, hasUserChoice, setOpenMode, setDefaultOpenMode]);

  const openIssueInTab = useCallback((issueId: string, activate = true) => {
    if (!supportsTabs || !navigation.openInNewTab) return false;
    navigation.openInNewTab(paths.issueDetail(issueId), issueId, { activate });
    return true;
  }, [supportsTabs, navigation, paths]);

  const value = useMemo<IssueOpeningCapability>(() => ({
    supportsTabs,
    supportsPeek,
    openSidePreview,
    openMode: supportsTabs
      ? (!hasUserChoice || mode === "page"
        // No stored choice yet, or a legacy in-place full page: the island's
        // full-page experience is a new header tab (M-313).
        ? "tab"
        // A peek-only host (no side panel) still migrates the peek to a tab.
        : mode === "peek" && !supportsPeek
          ? "tab"
          : mode)
      : mode === "tab"
        ? "page"
        : mode,
    openIssueInTab,
  }), [supportsTabs, supportsPeek, openSidePreview, hasUserChoice, mode, openIssueInTab]);

  return (
    <IssueOpeningCapabilityContext.Provider value={value}>
      {children}
    </IssueOpeningCapabilityContext.Provider>
  );
}

export function useIssueOpeningCapability() {
  return useContext(IssueOpeningCapabilityContext);
}
