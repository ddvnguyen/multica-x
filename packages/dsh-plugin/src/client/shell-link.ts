/**
 * Resolving what the header's side-panel toggle addresses (M-159, M-316).
 *
 * One Multica Project equals one dsh Workspace (a folder). The folder comes
 * from a chain, because the header is shared by every MMX view: the picked
 * project's `local_directory` resource, else the viewed issue's project, else
 * the one project the board/list filter names, else the host's
 * `workspaceFolder` config — empty meaning none. For a folder the bridge
 * matches the dsh Workspace by normalized path — creating one when none exists
 * (`workspaces.create` is idempotent) — and binds the Session the sidebar
 * surface works on: the on-screen one when it belongs to that Workspace,
 * otherwise the Workspace's first Session, otherwise a new Session in it.
 *
 * A press never navigates: MMX keeps the main column, or the button stays
 * disabled. The only places a dsh Sidebar can be drawn are beside MMX
 * (`hostPanel`) or over an on-screen Session, and the second one means leaving
 * MMX — exactly what the reader must not get from this control.
 *
 * Everything here is pure: the bridges carry real services, this module only
 * decides. Tests cover each rule separately.
 */

/** A project resource's `local_directory` ref subset the folder logic reads. */
export interface LocalDirectoryRefLike {
  daemon_id?: string;
  local_path?: string;
  execution_mode?: string;
}

/** A project resource subset the folder logic reads. */
export interface ProjectResourceLike {
  resource_type: unknown;
  resource_ref: unknown;
}

/**
 * The folder one project's resources name, after the daemon filter.
 *
 * Only `local_directory` refs address a folder on this host. A resource known
 * to run on another daemon names a folder we cannot open files for, so it is
 * not used; the first remaining ref wins. `in_place` mode is the one that
 * actually edits the user's working copy — a `worktree` ref names a runtime
 * copy, not the folder the sidebar's Session owns — so it is skipped.
 *
 * @param resources - the project's resources, in server order.
 * @param daemonId - this host's daemon id; a ref on a different daemon is
 *   skipped. Empty means the host cannot name its daemon, so no ref is
 *   filtered (permissive by default).
 */
export function folderOfProjectResources(
  resources: readonly ProjectResourceLike[],
  daemonId: string,
): string | null {
  const mine = daemonId.trim().toLowerCase();
  for (const resource of resources) {
    if (resource.resource_type !== "local_directory") continue;
    const ref: unknown = resource.resource_ref;
    if (typeof ref !== "object" || ref === null) continue;
    const record = ref as LocalDirectoryRefLike;
    if (record.execution_mode === "worktree") continue;
    if (
      mine !== "" &&
      typeof record.daemon_id === "string" &&
      record.daemon_id.trim().toLowerCase() !== mine
    ) {
      continue;
    }
    const path = typeof record.local_path === "string" ? record.local_path.trim() : "";
    if (path !== "") return path;
  }
  return null;
}

/** Which rung of the chain answered, for the tooltip and the disabled copy. */
export type FolderReason =
  | "project"
  | "issue-project"
  | "view-project"
  | "default"
  | "project-no-folder"
  | "no-folder";

export interface FolderSelectionInput {
  /** The header picker's project, or `null` for "All projects". */
  projectId: string | null;
  /** The projects the picked id is resolved against. */
  projects: readonly { id: string }[] | undefined;
  /**
   * One project id to its `local_directory`-eligible folder, or `null` when
   * that project has none. `undefined` means "not known yet" (resources still
   * loading, or an id this view does not address).
   */
  resourceFor: (projectId: string) => string | null | undefined;
  /** The host's default for "All projects"; may be empty. */
  workspaceFolder: string;
  /** The project the viewed issue belongs to; `null` when no issue is showing. */
  issueProjectId?: string | null;
  /**
   * The one project the current board/list filter names; `null` when the view
   * names none or more than one — an ambiguous context resolves nothing.
   */
  viewProjectId?: string | null;
}

/**
 * The folder the side-panel toggle addresses right now.
 *
 * The order is the reader's own context, most specific first: the project they
 * picked, the project the issue on screen belongs to, the single project the
 * board/list filter names, then the host's "All projects" default.
 *
 * A picked project is explicit intent, so a picked project without a folder is
 * reported as such rather than silently replaced by another rung — the header
 * would otherwise open a folder that contradicts its own picker. The two
 * derived rungs are context, not intent: when their project has no folder the
 * chain keeps going, and if nothing resolves the missing-folder copy names the
 * project that had no folder.
 */
export function folderForSelection(input: FolderSelectionInput): {
  folder: string | null;
  reason: FolderReason;
} {
  const folderOf = (id: string | null | undefined): string | null => {
    const trimmed = id?.trim() ?? "";
    if (trimmed === "") return null;
    const value = input.resourceFor(trimmed);
    return value === undefined || value === null || value === "" ? null : value;
  };

  if (input.projectId !== null) {
    const known =
      input.projects === undefined ||
      input.projects.some((project) => project.id === input.projectId);
    if (!known) return { folder: null, reason: "no-folder" };
    const picked = folderOf(input.projectId);
    if (picked !== null) return { folder: picked, reason: "project" };
    return { folder: null, reason: "project-no-folder" };
  }

  // The viewed issue's project, then the board/list context. Both are read the
  // same way and both fall through when their project has no folder.
  const derived: { id: string | null | undefined; reason: FolderReason }[] = [
    { id: input.issueProjectId, reason: "issue-project" },
    { id: input.viewProjectId, reason: "view-project" },
  ];
  let namedWithoutFolder = false;
  for (const rung of derived) {
    const id = rung.id?.trim() ?? "";
    if (id === "") continue;
    const folder = folderOf(id);
    if (folder !== null) return { folder, reason: rung.reason };
    namedWithoutFolder = true;
  }

  const fallback = input.workspaceFolder.trim();
  if (fallback !== "") return { folder: fallback, reason: "default" };
  return {
    folder: null,
    reason: namedWithoutFolder ? "project-no-folder" : "no-folder",
  };
}

/**
 * Whether the dsh sidebar has a session surface its commands can act on.
 *
 * The controller's own writes (`require()` in the ui-sidebar-right service)
 * throw `no session surface is mounted` precisely when the mounted snapshot
 * carries no Session, so this is the decision the buttons make instead of
 * re-throwing: a false answer predicts the next write's throw. Reads stay
 * available either way — `isExpanded` answers for the no-session case —
 * which is why only writes check.
 */
export function sessionSurfaceMounted(
  sidebarRight: { mounted: { getSnapshot(): string | undefined } } | null | undefined,
): boolean {
  return sidebarRight?.mounted.getSnapshot() !== undefined;
}

/** The words the dsh sidebar face rejects a write with when no surface is mounted. */
export const SIDEBAR_NO_SESSION_MESSAGE = "no session surface is mounted";

/**
 * The failure the dsh face raises for a write with no Session on screen.
 *
 * The hosted press raises this exact error rather than a new message, so one
 * note path covers both the pre-check and the face's own throw.
 */
export function noSessionSurfaceError(): Error {
  return new Error(`sidebarRight: ${SIDEBAR_NO_SESSION_MESSAGE}`);
}

/**
 * Whether an open or toggle died on exactly the no-session-surface state.
 *
 * Checked after the call, because a session can leave between the pre-check
 * and the click; both outcomes mean the same user-facing note, so the caller
 * maps this to the `no-session` tooltip copy instead of the raw error text.
 */
export function isNoSessionSurfaceError(cause: unknown): boolean {
  return cause instanceof Error && cause.message.includes(SIDEBAR_NO_SESSION_MESSAGE);
}

export type DisabledReason = FolderReason | "no-session" | "host";

/**
 * Why the button is dead on a dsh build without card C's `hostPanel`.
 *
 * A host without it can only draw the Sidebar over an on-screen Session, which
 * means replacing MMX with the dsh page. The control never does that — the
 * reader asked for the panel *beside* MMX — so it says what the build cannot do
 * instead of navigating.
 */
export const HOST_NO_BESIDE_PANEL_TOOLTIP =
  "This dsh build cannot show the side panel next to MMX";

/** The copy a press that found no Session surface to draw surfaces. */
export const NO_SESSION_SURFACE_NOTE =
  "The dsh side panel needs a session in the project folder";

/**
 * The tooltip copy for one reason, the way the disabled state reads.
 *
 * The no-session throw is shell state, not a glitch — the hosted column has
 * nothing to draw for until a Session is bound to it — so it says why the way
 * the disabled state does, instead of the controller's raw error text leaking
 * into the header.
 */
export function describe(reason: DisabledReason, folder: string | null = null): string {
  if (reason === "no-session") return NO_SESSION_SURFACE_NOTE;
  return disabledTooltip(reason, folder);
}

/** The tooltip for the disabled toggle. */
export function disabledTooltip(
  reason: DisabledReason,
  folder: string | null,
): string {
  switch (reason) {
    case "project":
    case "issue-project":
    case "view-project":
    case "default":
      return "Open the dsh side panel";
    case "host":
      return HOST_NO_BESIDE_PANEL_TOOLTIP;
    case "project-no-folder":
      return "Link a folder to this project";
    case "no-folder":
      return "Choose a project";
    case "no-session":
      return folder === null
        ? "Link a folder to this project"
        : `Open a dsh session in ${folder}`;
  }
}

/** Path equality in the way dsh hosts name directories: `/`-collapsed, trailing `/` gone. */
export function normalizePath(path: string): string {
  const trimmed = path.trim();
  if (trimmed === "") return "";
  const collapsed = trimmed.endsWith("/")
    ? trimmed.replace(/\/+$/, "")
    : trimmed;
  return collapsed.replace(/\/{2,}/g, "/");
}

/** Subset of the dsh Workspace view the match reads. */
export interface WorkspaceLike {
  workspaceId: string;
  path: string;
  sessionIds: readonly string[];
}

/** Subset of the dsh Session view the binding reads. */
export interface SessionLike {
  id: string;
  cwd?: string;
}

/**
 * The dsh Workspace for one folder: the one whose path matches, else `null`
 * (the caller creates one — a dsh Workspace is never deleted here).
 */
export function workspaceForFolder(
  workspaces: readonly WorkspaceLike[],
  folder: string,
): WorkspaceLike | null {
  const want = normalizePath(folder);
  if (want === "") return null;
  return workspaces.find((entry) => normalizePath(entry.path) === want) ?? null;
}

/**
 * The Session the sidebar surface opens the tab under.
 *
 * The on-screen Session when it belongs to the Workspace (the Owner's bind
 * rule), else the Workspace's first Session, else `null` — the caller creates
 * one in the Workspace.
 */
export function boundSessionId(
  mountedSessionId: string | undefined,
  workspace: Pick<WorkspaceLike, "sessionIds">,
): string | null {
  const mounted = mountedSessionId?.trim();
  if (mounted && workspace.sessionIds.includes(mounted)) return mounted;
  const first = workspace.sessionIds[0];
  return first === undefined ? null : first;
}

/**
 * What one press of the header's panel toggle does.
 *
 * `toggle` flips the Sidebar that is already on screen — the same call dsh's
 * own header control makes, so the reader gets the column, its tabs and its
 * Start guide exactly as dsh draws them. `retarget` is the hosted case: the
 * column is drawn beside MMX, and it has to be told which Session's folder it
 * edits before it opens, or Files and Terminal would point at whichever
 * Session the Conversation last had. `unavailable` is a host that cannot draw
 * the column beside MMX: there is no press that keeps MMX, so nothing happens.
 */
export type ToggleIntent =
  | { kind: "toggle" }
  | { kind: "retarget"; sessionId: string | null }
  | { kind: "unavailable" };

/**
 * Plan the call one press makes. Never a navigation.
 *
 * The column always shows the Session bound to the project's folder, and it is
 * only ever drawn beside MMX. With card C it renders there, so the press names
 * that Session (`selectHostedSession`) and flips the column, leaving the main
 * column where it is. Without `hostPanel` the press is `unavailable`: the only
 * other surface is over an on-screen Session, and putting one there replaces
 * MMX with the dsh page — the one outcome the reader has ruled out. A host
 * that hosts the panel but cannot name a Session is left to toggle what it
 * shows, since moving the column would mean leaving MMX.
 */
export function planSidebarToggle(input: {
  hasHostPanel: boolean;
  canNameSession: boolean;
  mountedSessionId: string | undefined;
  boundSessionId: string | null;
}): ToggleIntent {
  if (!input.hasHostPanel) return { kind: "unavailable" };
  const mounted = input.mountedSessionId?.trim();
  const onScreen = mounted !== undefined && mounted !== "";
  // The column already edits the project's folder: flip it, nothing to move.
  if (onScreen && input.boundSessionId === mounted) return { kind: "toggle" };
  if (input.canNameSession) {
    return { kind: "retarget", sessionId: input.boundSessionId };
  }
  // A card C build before naming shipped: the column has no way to move, and
  // moving it any other way would replace MMX with the dsh page.
  return { kind: "toggle" };
}

/**
 * The toggle's tooltip: what a press does, and on which folder.
 *
 * @param expanded - whether the Sidebar is shown right now (pressed state).
 * @param folder - the folder the column would show; never null when enabled.
 */
export function panelToggleTitle(expanded: boolean, folder: string): string {
  return expanded ? "Close the dsh side panel" : `Open the dsh side panel on ${folder}`;
}

/**
 * The rendered state of the toggle: enabled only when a press can keep MMX.
 *
 * Two independent facts decide it. The host has to be able to draw the panel
 * beside MMX (`canHostPanel`), and the current view has to resolve a folder
 * (`folder`). When either fails the button is disabled, and the tooltip says
 * which one — the host's limit first, since no view can fix it.
 *
 * @param input.reason - the chain's answer, for the folder-side copy.
 * @param input.busy - a press is in flight.
 * @param input.expanded - whether the column is shown (pressed state).
 */
export function toggleState(input: {
  folder: string | null;
  reason: FolderReason;
  canHostPanel: boolean;
  busy?: boolean;
  expanded?: boolean;
}): { disabled: boolean; title: string } {
  if (!input.canHostPanel) {
    return { disabled: true, title: HOST_NO_BESIDE_PANEL_TOOLTIP };
  }
  if (input.folder === null) {
    return { disabled: true, title: disabledTooltip(input.reason, null) };
  }
  if (input.busy === true) {
    return { disabled: true, title: panelToggleTitle(input.expanded === true, input.folder) };
  }
  return {
    disabled: false,
    title: panelToggleTitle(input.expanded === true, input.folder),
  };
}

/** Structural subset of the dsh `sidebarRight` face the bridge uses. */
export interface SidebarRightLike {
  readonly mounted: {
    getSnapshot(): string | undefined;
    subscribe(listener: () => void): () => void;
  };
  toggleExpanded(): void;
  isExpanded(): boolean;
  /** Card C publishes the same fact as a subscribable; absent before it ships. */
  expanded?: {
    getSnapshot(): boolean;
    subscribe(listener: () => void): () => void;
  };
  /** Card C's opt-in: the sidebar beside a hosted `main` panel. Optional face. */
  hostPanel?(panelId: string): () => void;
  /**
   * Card C's target for the hosted column; absent before it ships. Naming a
   * Session is what makes Files and Terminal open on the project's folder
   * while MMX keeps the main column.
   */
  selectHostedSession?(sessionId: string): () => void;
}

/** Structural subset of the dsh `workspaces` face the bridge uses. */
export interface WorkspacesLike {
  readonly list: {
    getSnapshot(): { items: readonly WorkspaceLike[] };
    subscribe(listener: () => void): () => void;
  };
  create(input: { path: string }): Promise<WorkspaceLike>;
}

/** Structural subset of the dsh `sessions` face the bridge uses. */
export interface SessionsLike {
  readonly list: {
    getSnapshot(): { byId: Record<string, SessionLike> };
    subscribe(listener: () => void): () => void;
  };
  create(opts?: { workspaceId?: string; cwd?: string }): Promise<string>;
}
