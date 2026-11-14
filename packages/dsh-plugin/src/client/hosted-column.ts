import {
  boundSessionId,
  describe,
  noSessionSurfaceError,
  planSidebarToggle,
  sessionSurfaceMounted,
  workspaceForFolder,
  type SessionsLike,
  type SidebarRightLike,
  type WorkspacesLike,
} from "./shell-link";

/**
 * Prepare dsh's right column to act on one project folder.
 *
 * Two callers need exactly this: the header's panel toggle (M-159), which then
 * flips the column, and the side preview (M-336), which opens a page tab in it.
 * Both run the same three steps — match the Workspace (creating it, which dsh
 * makes idempotent), name the Session its Files and Terminal should edit, and
 * fail loudly when the column has no surface to act on — so the sequence lives
 * here once and neither caller can drift from the other.
 *
 * A host that cannot host the column beside MMX throws the toggle's own copy:
 * the only other place its Sidebar can be drawn replaces MMX with the dsh page,
 * which is the outcome the reader ruled out.
 */

/** The shell faces the column needs. */
export interface HostedColumnFaces {
  sidebarRight: SidebarRightLike;
  workspaces: WorkspacesLike | null;
  sessions: SessionsLike | null;
}

/**
 * The name last given to the hosted column, released when a press replaces it.
 *
 * Page-lifetime, like the rest of this bridge: one name is in force at a time,
 * and a press on another project's folder must withdraw the old one rather
 * than leave the column on the workspace it no longer edits.
 */
let releaseHostedSession: (() => void) | null = null;

/** Tell the hosted column which Session's folder it edits. */
function nameHostedSession(sidebarRight: SidebarRightLike, sessionId: string): void {
  const name = sidebarRight.selectHostedSession;
  if (typeof name !== "function") {
    throw new Error("this dsh build cannot retarget the side panel");
  }
  releaseHostedSession?.();
  releaseHostedSession = name.call(sidebarRight, sessionId);
}

/**
 * Name the column's Session for `folder`, creating the Workspace or Session
 * when the lane has neither. Throws when this build cannot draw the column
 * beside MMX, or when the column has no Session surface to act on.
 *
 * @param faces - the shell faces read from the plugin context.
 * @param folder - the project folder the column should address.
 */
export async function prepareHostedColumn(
  faces: HostedColumnFaces,
  folder: string,
): Promise<void> {
  const { sidebarRight, workspaces, sessions } = faces;
  if (workspaces === null || sessions === null) {
    throw new Error("this dsh build has no workspace or session service");
  }
  // Match or create (never delete): dsh Workspace registration is idempotent on
  // the path, so a repeated press converges.
  let workspace = workspaceForFolder(workspaces.list.getSnapshot().items, folder);
  if (workspace === null) {
    workspace = await workspaces.create({ path: folder });
  }
  const mountedSessionId = sidebarRight.mounted.getSnapshot();
  const intent = planSidebarToggle({
    hasHostPanel: typeof sidebarRight.hostPanel === "function",
    canNameSession: typeof sidebarRight.selectHostedSession === "function",
    mountedSessionId,
    boundSessionId: boundSessionId(mountedSessionId, workspace),
  });
  if (intent.kind === "unavailable") throw new Error(describe("host"));
  if (intent.kind === "toggle") return;
  let sessionId = intent.sessionId;
  if (sessionId === null || sessionId === "") {
    sessionId = await sessions.create({ workspaceId: workspace.workspaceId });
  }
  nameHostedSession(sidebarRight, sessionId);
  if (!sessionSurfaceMounted(sidebarRight)) throw noSessionSurfaceError();
}
