import { prepareHostedColumn, type HostedColumnFaces } from "./hosted-column";
import type { SidebarRightLike } from "./shell-link";

/**
 * The side preview tab type (M-336, generalized in M-336 G6).
 *
 * `Side preview` is a tab in dsh's right column: the plugin registers one page
 * type, the island opens it with navigation params describing what to show, and
 * the tab body hands the column a container the island portals the matching
 * side stack into.
 */

/** The tab kind `openTab` names (kept for compatibility). */
export const SIDE_PREVIEW_KIND = "multica-issue";

/** This implementation's identity: the key its body registers under. */
export const SIDE_PREVIEW_TAB_ID = "@multica/dsh-plugin";

/** The chip's text, captured once per tab by dsh. */
export const SIDE_PREVIEW_TITLES = { title: "Preview" } as const;

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

/** The issue a tab is showing (compat helper). */
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
