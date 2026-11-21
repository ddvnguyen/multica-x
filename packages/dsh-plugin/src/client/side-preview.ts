import { prepareHostedColumn, type HostedColumnFaces } from "./hosted-column";
import type { SidebarRightLike } from "./shell-link";

/**
 * The side preview tab type (M-336, generalized in G6).
 *
 * `Side preview` used to be the fork's floating peek inside MMX's own column —
 * the surface the Owner rejected (M-313). It is now a tab in dsh's right
 * column: the plugin registers one page type, the island opens it with the
 * target as its navigation params, and the tab body hands the column a
 * container the island portals the matching side stack into.
 *
 * The chip is generic on purpose. dsh captures `title(address)` when the tab
 * opens and never rewrites it, and the only address a page type has is
 * `sidebar://<kind>` — so the identifier belongs in the body's header, where it
 * can follow a reveal that hands the same tab another target.
 */

/** The tab kind `openTab` names (kept for compatibility). */
export const SIDE_PREVIEW_KIND = "multica-issue";

/** This implementation's identity: the key its body registers under. */
export const SIDE_PREVIEW_TAB_ID = "@multica/dsh-plugin";

/** The chip's text, captured once per tab by dsh. */
export const SIDE_PREVIEW_TITLES = { title: "Preview" } as const;

/** Last target survives dsh restoring the tab shell without its navigation params. */
export const SIDE_PREVIEW_LAST_TARGET_KEY = "multica.dsh.side-preview-target";

export type SidePreviewView = "issue" | "agent" | "runtime" | "skill" | "project";

export interface SidePreviewTargetRef {
  view: SidePreviewView;
  id: string;
}

function cleanId(value: unknown): string | null {
  return typeof value === "string" && value.trim() !== "" ? value.trim() : null;
}

/** The target a tab is showing, read out of dsh's navigation params. */
export function sidePreviewTargetFromParams(params: unknown): SidePreviewTargetRef | null {
  if (params === null || typeof params !== "object") return null;
  const record = params as { view?: unknown; id?: unknown; issueId?: unknown };
  const view = record.view;
  const id = cleanId(record.id);
  if (
    (view === "issue" || view === "agent" || view === "runtime" || view === "skill" || view === "project") &&
    id !== null
  ) {
    return { view, id };
  }
  // Back-compat: original issue-only shape.
  const issueId = cleanId(record.issueId);
  if (issueId !== null) return { view: "issue", id: issueId };
  return null;
}

/** Store the most recently opened target for dsh's parameter-less tab restore. */
export function rememberSidePreviewTarget(target: SidePreviewTargetRef): void {
  try {
    globalThis.localStorage?.setItem(SIDE_PREVIEW_LAST_TARGET_KEY, JSON.stringify(target));
  } catch {
    // Storage can be unavailable in restricted browser contexts; live tab params still work.
  }
}

/** Prefer the current tab params; use the saved target only for an empty restore. */
export function sidePreviewTargetFromParamsOrLast(params: unknown): SidePreviewTargetRef | null {
  const target = sidePreviewTargetFromParams(params);
  if (target !== null) return target;
  try {
    const saved = globalThis.localStorage?.getItem(SIDE_PREVIEW_LAST_TARGET_KEY);
    return saved === null || saved === undefined ? null : sidePreviewTargetFromParams(JSON.parse(saved));
  } catch {
    return null;
  }
}

/** The issue a tab is showing (compat helper), read out of dsh's navigation params. */
export function issueIdFromParams(params: unknown): string | null {
  const target = sidePreviewTargetFromParams(params);
  return target?.view === "issue" ? target.id : null;
}

/** What one open of the preview needs from the page around it. */
export interface SidePreviewOpenInput extends HostedColumnFaces {
  /** The folder the hosted column addresses; `null` when no project resolves. */
  folder: string | null;
}

/** The opener the island's capability calls for one press (issue compat). */
export type SidePreviewOpener = (issueId: string) => Promise<void>;

/** Generic opener for any supported side-preview view. */
export type SidePreviewTargetOpener = (target: SidePreviewTargetRef) => Promise<void>;

/**
 * Open a preview target in dsh's right column.
 *
 * Two steps, both required: the column has to have a Session surface to act on
 * (a hosted panel with no Session cannot be opened into), and the tab type has
 * to be registered for `openTab` to find — it is, when this module runs at all.
 * `revealIfOpened` is the default and the point: a second press on another
 * target focuses the one preview tab and hands it the new id, so the column
 * never fills with copies of itself.
 *
 * @throws when the column cannot be prepared, or the shell has no `openTab`.
 */
async function openTarget(
  input: SidePreviewOpenInput,
  target: SidePreviewTargetRef,
): Promise<void> {
  const { sidebarRight, folder } = input;
  const openTab = sidebarRight.openTab;
  if (typeof openTab !== "function") {
    throw new Error("this dsh build cannot host the side preview");
  }
  if (folder === null) throw new Error("this view names no project folder");
  await prepareHostedColumn(input, folder);
  rememberSidePreviewTarget(target);
  openTab.call(sidebarRight, SIDE_PREVIEW_KIND, {
    params: { view: target.view, id: target.id },
    revealIfOpened: true,
  });
}

/** Open the issue preview in dsh's right column (compat). */
export function createSidePreviewOpener(input: SidePreviewOpenInput): SidePreviewOpener {
  return async (issueId: string): Promise<void> => {
    await openTarget(input, { view: "issue", id: issueId });
  };
}

/** Open any supported preview target in dsh's right column. */
export function createSidePreviewTargetOpener(input: SidePreviewOpenInput): SidePreviewTargetOpener {
  return async (target: SidePreviewTargetRef): Promise<void> => {
    await openTarget(input, target);
  };
}

/** Whether this host can show the preview at all (seam present). */
export function canOpenSidePreview(
  sidebarRight: SidebarRightLike | null,
  tabTypesRegistered: boolean,
): boolean {
  return tabTypesRegistered && typeof sidebarRight?.openTab === "function";
}
