import { useMemo, useRef, type ReactNode } from "react";
import { IssueOpeningCapabilityProvider } from "../fork";
import { hostShell } from "./shell-runtime";
import { createSidePreviewTargetOpener, type SidePreviewTargetRef } from "./side-preview";
import { useSidebarFolder, type SidebarFolderInput } from "./sidebar-folder";

type OpenTarget = string | SidePreviewTargetRef;

function toTarget(target: OpenTarget): SidePreviewTargetRef {
  return typeof target === "string" ? { view: "issue", id: target } : target;
}

/**
 * `Side preview` as the island can offer it (M-336, G6).
 *
 * The press is synchronous while preparing the column is not: a folder with no
 * dsh Workspace yet has one created, and a Workspace with no Session gets one.
 * The opener answers "handled" at once and opens the tab when it lands.
 */
export function useSidePreview(input: SidebarFolderInput): {
  available: boolean;
  open: (target: OpenTarget) => boolean;
} {
  const shell = hostShell();
  const { folder } = useSidebarFolder(input);
  const busy = useRef(false);

  const available = shell !== null && shell.sidebarRight !== null && shell.sidePreviewKind !== null;

  return useMemo(() => {
    const open = (target: OpenTarget): boolean => {
      const sidebarRight = shell?.sidebarRight ?? null;
      if (shell === null || sidebarRight === null || shell.sidePreviewKind === null) return false;
      // No project folder means no Session for the column to act on. The click
      // keeps its other meaning (a new tab) rather than dying silently.
      if (folder === null) return false;
      if (busy.current) return true;
      busy.current = true;
      const opener = createSidePreviewTargetOpener({
        sidebarRight,
        workspaces: shell.workspaces,
        sessions: shell.sessions,
        panelInfo: shell.panelInfo,
        folder,
      });
      void opener(toTarget(target))
        .catch((cause: unknown) => {
          console.warn(
            "[multica] side preview:",
            cause instanceof Error ? cause.message : String(cause),
          );
        })
        .finally(() => {
          busy.current = false;
        });
      return true;
    };
    return { available, open };
  }, [available, folder, shell]);
}

/**
 * The capability provider the island mounts, with the host's side preview.
 *
 * `supportsPeek` stays on in both branches. With dsh's tab registry the preview
 * is the host's column and `openSidePreview` is what lands the click there —
 * `IssuePeekHost` sees it and stays down, so the floating panel never mounts
 * beside a column that already shows the same target. Without the registry there
 * is no column to open, `openSidePreview` is `null`, and the fork's floating
 * peek is the preview.
 */
export function SidePreviewCapability({
  input,
  children,
}: {
  input: SidebarFolderInput;
  children: ReactNode;
}) {
  const preview = useSidePreview(input);
  return (
    <IssueOpeningCapabilityProvider
      supportsTabs
      supportsPeek
      openSidePreview={preview.available ? preview.open : null}
    >
      {children}
    </IssueOpeningCapabilityProvider>
  );
}
