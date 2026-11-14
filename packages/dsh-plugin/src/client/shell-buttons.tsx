import { useEffect, useState } from "react";
import { PanelRight, PanelRightClose } from "../fork";
import type { IslandRoute } from "./navigation";
import { hostShell, type ShellBridge } from "./shell-runtime";
import { prepareHostedColumn } from "./hosted-column";
import { useSidebarFolder } from "./sidebar-folder";
import {
  describe,
  isNoSessionSurfaceError,
  toggleState,
  type SidebarRightLike,
} from "./shell-link";

/**
 * The header's side-panel toggle (M-159, M-316): the dsh right Sidebar, opened
 * from MMX the way dsh's own session header opens it.
 *
 * It is one icon button, not a control per sidebar view: the column owns its
 * tabs, its Start guide and its collapse, exactly as it does beside a
 * Conversation, so MMX has nothing to name beyond "show it". It sits in the
 * header the island draws once for every view, so the folder it addresses is
 * resolved per view (`folderForSelection`): the picked project, else the viewed
 * issue's project, else the one project the board filter names, else the host's
 * `workspaceFolder`. No folder means the button is disabled with the copy that
 * says what is missing.
 *
 * A press either flips the column on screen (`toggle`) or names the project's
 * Session for the column that is drawn beside MMX (`retarget`). It never
 * navigates: a host whose build cannot host the column beside MMX has the
 * button disabled, because the only other place its Sidebar can be drawn is
 * over an on-screen Session — which replaces MMX with the dsh page, the exact
 * outcome the reader ruled out.
 *
 * The button renders only when `apply` installed a bridge — the one host fact
 * this island cannot know without the shell, so its absence is exactly the
 * "no right sidebar" case. A failed press stays in the tooltip rather than in
 * the header: shell state is not a page error, and the header is a strip.
 */
export function ShellButtons({
  workspaceId,
  projects,
  projectId,
  workspaceFolder,
  route,
}: {
  workspaceId: string;
  projects: readonly { id: string }[] | undefined;
  projectId: string | null;
  workspaceFolder: string;
  /** The tab on screen; the viewed issue's project is one rung of the chain. */
  route: IslandRoute;
}) {
  const shell = hostShell();
  if (shell === null || shell.sidebarRight === null) return null;
  return <SidePanelToggle
    shell={shell}
    workspaceId={workspaceId}
    projects={projects}
    projectId={projectId}
    workspaceFolder={workspaceFolder}
    route={route}
  />;
}

/** The Sidebar's shown state, read tolerantly: a face without it reads closed. */
function readExpanded(sidebarRight: SidebarRightLike | null): boolean {
  try {
    return sidebarRight?.isExpanded() ?? false;
  } catch {
    return false;
  }
}

function SidePanelToggle({
  shell,
  workspaceId,
  projects,
  projectId,
  workspaceFolder,
  route,
}: {
  shell: ShellBridge;
  workspaceId: string;
  projects: readonly { id: string }[] | undefined;
  projectId: string | null;
  workspaceFolder: string;
  route: IslandRoute;
}) {
  const sidebarRight = shell.sidebarRight;

  // The pressed state follows the controller, not this button: the reader can
  // collapse the column from its own header too. Card C publishes `expanded`
  // as a subscribable; without it the button tracks its own presses only.
  const [expanded, setExpanded] = useState<boolean>(() => readExpanded(sidebarRight));
  useEffect(() => {
    const observable = sidebarRight?.expanded;
    if (observable === undefined) return;
    setExpanded(observable.getSnapshot());
    return observable.subscribe(() => setExpanded(observable.getSnapshot()));
  }, [sidebarRight]);

  // Which folder the column addresses right now; the side preview asks the
  // same question through the same hook (M-336).
  const resolved = useSidebarFolder({
    workspaceId,
    projects,
    projectId,
    workspaceFolder,
    route,
    daemonId: shell.daemonId,
  });

  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<string | null>(null);

  async function onToggle(): Promise<void> {
    const { workspaces, sessions } = shell;
    if (sidebarRight === null) return;
    const folder = resolved.folder;
    if (folder === null || workspaces === null || sessions === null) return;
    // A press on an open column closes it where it is. Retargeting first would
    // swap the Session under a column the reader is closing.
    if (readExpanded(sidebarRight)) {
      sidebarRight.toggleExpanded();
      setExpanded(false);
      setNote(null);
      return;
    }
    setBusy(true);
    try {
      // Match or create the Workspace, name the project's Session, and fail on
      // a build that cannot host the column beside MMX. The side preview runs
      // the same sequence, so it lives in one place (M-336).
      await prepareHostedColumn({ sidebarRight, workspaces, sessions }, folder);
      if (!readExpanded(sidebarRight)) sidebarRight.toggleExpanded();
      setExpanded(readExpanded(sidebarRight));
      setNote(null);
    } catch (cause: unknown) {
      setNote(
        isNoSessionSurfaceError(cause)
          ? describe("no-session")
          : cause instanceof Error
            ? cause.message
            : String(cause),
      );
    } finally {
      setBusy(false);
    }
  }

  const state = toggleState({
    folder: resolved.folder,
    reason: resolved.reason,
    canHostPanel: typeof sidebarRight?.hostPanel === "function",
    busy,
    expanded,
  });
  const title = state.title;
  const glyph = expanded
    ? <PanelRightClose className="size-4" />
    : <PanelRight className="size-4" />;

  return (
    <button
      type="button"
      data-shell-button="toggle"
      data-shell-note={note ?? undefined}
      aria-label={note ?? title}
      aria-pressed={expanded}
      disabled={state.disabled}
      title={note ?? title}
      className={panelButtonClassName}
      onClick={() => void onToggle()}
    >
      {glyph}
    </button>
  );
}

const panelButtonClassName =
  "h-7 w-7 shrink-0 items-center justify-center rounded-md border border-border text-foreground " +
  "flex disabled:cursor-not-allowed disabled:opacity-50";
