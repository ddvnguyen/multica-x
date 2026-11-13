import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  PanelRight,
  PanelRightClose,
  projectResourcesOptions,
  useCanonicalIssue,
  useViewStore,
} from "../fork";
import type { IslandRoute } from "./navigation";
import { hostShell, type ShellBridge } from "./shell-runtime";
import {
  boundSessionId,
  describe,
  folderForSelection,
  folderOfProjectResources,
  isNoSessionSurfaceError,
  noSessionSurfaceError,
  planSidebarToggle,
  sessionSurfaceMounted,
  toggleState,
  workspaceForFolder,
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

/**
 * The name last given to the hosted column, released when a press replaces it.
 *
 * Page-lifetime, like the rest of this bridge: one name is in force at a time,
 * and a press on another project's folder must withdraw the old one rather
 * than leave the column on the workspace it no longer edits.
 */
let releaseHostedSession: (() => void) | null = null;

/**
 * Tell the hosted column which Session's folder it edits.
 *
 * @param sidebarRight - the face that names the column's Session.
 * @param sessionId - the Session bound to the project's folder.
 */
function nameHostedSession(sidebarRight: SidebarRightLike, sessionId: string): void {
  const name = sidebarRight.selectHostedSession;
  if (typeof name !== "function") {
    throw new Error("this dsh build cannot retarget the side panel");
  }
  releaseHostedSession?.();
  releaseHostedSession = name.call(sidebarRight, sessionId);
}

/** The Sidebar's shown state, read tolerantly: a face without it reads closed. */
function readExpanded(sidebarRight: SidebarRightLike | null): boolean {
  try {
    return sidebarRight?.isExpanded() ?? false;
  } catch {
    return false;
  }
}

/**
 * One project's folder, or `undefined` while its resources are not known.
 *
 * `projectId === null` addresses nothing and the query stays disabled, so the
 * header costs one request per distinct project a view actually names — and
 * none for "All projects" on a board with no filter.
 */
function useProjectFolder(
  workspaceId: string,
  projectId: string | null,
  daemonId: string,
): string | null | undefined {
  const { data } = useQuery({
    ...projectResourcesOptions(workspaceId, projectId ?? ""),
    enabled: projectId !== null,
  });
  if (projectId === null || data === undefined) return undefined;
  return folderOfProjectResources(data, daemonId);
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

  // The viewed issue's project is the second rung (M-316): an issue page whose
  // picker still says "All projects" belongs to exactly one project, and the
  // toggle must work there. The identifier resolution shares the detail page's
  // own request, so this is one cache entry, not a second read.
  const issueSegment = route.view === "issue" ? route.issueId : null;
  const { issue } = useCanonicalIssue(workspaceId, issueSegment ?? "", {
    enabled: issueSegment !== null,
  });
  const issueProjectId = issueSegment === null ? null : (issue?.project_id ?? null);

  // The board/list filter is the third rung: one project named by the filter is
  // unambiguous context, more than one is not, and no other view has it.
  const projectFilters = useViewStore((state) => state.projectFilters);
  const viewProjectId =
    route.view === "board" && projectFilters.length === 1
      ? (projectFilters[0] ?? null)
      : null;

  const pickedFolder = useProjectFolder(workspaceId, projectId, shell.daemonId);
  const issueProjectFolder = useProjectFolder(workspaceId, issueProjectId, shell.daemonId);
  const viewProjectFolder = useProjectFolder(workspaceId, viewProjectId, shell.daemonId);

  const resolved = useMemo(() => {
    // The picked project is set last: when two rungs name the same project, the
    // picker's own resources answer for it.
    const folders = new Map<string, string | null | undefined>();
    if (issueProjectId !== null) folders.set(issueProjectId, issueProjectFolder);
    if (viewProjectId !== null) folders.set(viewProjectId, viewProjectFolder);
    if (projectId !== null) folders.set(projectId, pickedFolder);
    return folderForSelection({
      projectId,
      projects,
      resourceFor: (id) => folders.get(id),
      workspaceFolder,
      issueProjectId,
      viewProjectId,
    });
  }, [
    projectId,
    projects,
    workspaceFolder,
    issueProjectId,
    viewProjectId,
    pickedFolder,
    issueProjectFolder,
    viewProjectFolder,
  ]);

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
      // Match or create (never delete): dsh Workspace registration is
      // idempotent on the path, so a repeated press converges.
      let workspace = workspaceForFolder(workspaces.list.getSnapshot().items, folder);
      if (workspace === null) {
        workspace = await workspaces.create({ path: folder });
      }
      const intent = planSidebarToggle({
        hasHostPanel: typeof sidebarRight.hostPanel === "function",
        canNameSession: typeof sidebarRight.selectHostedSession === "function",
        mountedSessionId: sidebarRight.mounted.getSnapshot(),
        boundSessionId: boundSessionId(sidebarRight.mounted.getSnapshot(), workspace),
      });
      if (intent.kind === "unavailable") {
        // The button is disabled for this host, so this only runs for a face
        // that lost `hostPanel` since it rendered. Nothing to open beside MMX,
        // and nothing here navigates. Let the next render disable it.
        setNote(describe("host"));
        return;
      }
      if (intent.kind === "retarget") {
        // The column is drawn beside MMX: name the project's Session so Files
        // and Terminal open on that folder, and flip the column in place. MMX
        // keeps the main column, so the board never leaves the screen.
        let sessionId = intent.sessionId;
        if (sessionId === null || sessionId === "") {
          sessionId = await sessions.create({ workspaceId: workspace.workspaceId });
        }
        nameHostedSession(sidebarRight, sessionId);
        if (!sessionSurfaceMounted(sidebarRight)) throw noSessionSurfaceError();
      }
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
