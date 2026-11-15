import { useMemo, useRef, type ReactNode } from "react";
import { IssueOpeningCapabilityProvider } from "../fork";
import { hostShell } from "./shell-runtime";
import { createSidePreviewOpener } from "./side-preview";
import { useSidebarFolder, type SidebarFolderInput } from "./sidebar-folder";

/**
 * `Side preview` as the island can offer it (M-336).
 *
 * The press is synchronous — it is a click on a board card — while preparing
 * the column is not: a folder with no dsh Workspace yet has one created, and a
 * Workspace with no Session gets one. So the opener answers "handled" at once,
 * marks the column in the background, and opens the tab when it lands. A press
 * that arrives while that is in flight is absorbed: two clicks on two cards in
 * the same second must not race two Session creations.
 *
 * `available` is the host fact the capability needs: a build without dsh's tab
 * registry has no column to open a preview in, and the island then falls back
 * to the fork's floating peek inside the MMX column. `Side preview` is offered
 * either way (M-330); only where it lands depends on the host.
 */
export function useSidePreview(input: SidebarFolderInput): {
  available: boolean;
  open: (issueId: string) => boolean;
} {
  const shell = hostShell();
  const { folder } = useSidebarFolder(input);
  const busy = useRef(false);

  const available = shell !== null && shell.sidebarRight !== null && shell.sidePreviewKind !== null;

  return useMemo(() => {
    const open = (issueId: string): boolean => {
      const sidebarRight = shell?.sidebarRight ?? null;
      if (shell === null || sidebarRight === null || shell.sidePreviewKind === null) return false;
      // No project folder means no Session for the column to act on. The click
      // keeps its other meaning (a new tab) rather than dying silently.
      if (folder === null) return false;
      if (busy.current) return true;
      busy.current = true;
      const opener = createSidePreviewOpener({
        sidebarRight,
        workspaces: shell.workspaces,
        sessions: shell.sessions,
        folder,
      });
      void opener(issueId)
        .catch((cause: unknown) => {
          // The header is a strip and the click already landed: shell state is
          // not a page error, so it goes to the console the plugin owns.
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
 * A component rather than a hook call at the call site because the folder
 * chain reads the view store, and that store's provider wraps the page tree:
 * the hook has to run below it, and so does this.
 *
 * `supportsPeek` stays on in both branches. With dsh's tab registry the preview
 * is the host's column and `openSidePreview` is what lands the click there —
 * `IssuePeekHost` sees it and stays down, so the floating panel never mounts
 * beside a column that already shows the same issue. Without the registry there
 * is no column to open, `openSidePreview` is `null`, and the fork's floating
 * peek is the preview (M-336, Owner decision 4).
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
