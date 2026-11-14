import { prepareHostedColumn, type HostedColumnFaces } from "./hosted-column";
import type { SidebarRightLike } from "./shell-link";

/**
 * The side preview tab type (M-336).
 *
 * `Side preview` used to be the fork's floating peek inside MMX's own column —
 * the surface the Owner rejected (M-313). It is now a tab in dsh's right
 * column: the plugin registers one page type, the island opens it with the
 * issue's id as its navigation params, and the tab body hands the column a
 * container the island portals the issue's side stack into.
 *
 * The chip is generic on purpose. dsh captures `title(address)` when the tab
 * opens and never rewrites it, and the only address a page type has is
 * `sidebar://<kind>` — so the identifier belongs in the body's header, where it
 * can follow a reveal that hands the same tab another issue.
 */

/** The tab kind `openTab` names. */
export const SIDE_PREVIEW_KIND = "multica-issue";

/** This implementation's identity: the key its body registers under. */
export const SIDE_PREVIEW_TAB_ID = "@multica/dsh-plugin";

/** The chip's text, captured once per tab by dsh. */
export const SIDE_PREVIEW_TITLES = { title: "Issue" } as const;

/** The issue a tab is showing, read out of dsh's navigation params. */
export function issueIdFromParams(params: unknown): string | null {
  if (params === null || typeof params !== "object") return null;
  const value = (params as { issueId?: unknown }).issueId;
  return typeof value === "string" && value.trim() !== "" ? value : null;
}

/** What one open of the preview needs from the page around it. */
export interface SidePreviewOpenInput extends HostedColumnFaces {
  /** The folder the hosted column addresses; `null` when no project resolves. */
  folder: string | null;
}

/** The opener the island's capability calls for one press. */
export type SidePreviewOpener = (issueId: string) => Promise<void>;

/**
 * Open the issue preview in dsh's right column.
 *
 * Two steps, both required: the column has to have a Session surface to act on
 * (a hosted panel with no Session cannot be opened into), and the tab type has
 * to be registered for `openTab` to find — it is, when this module runs at all.
 * `revealIfOpened` is the default and the point: a second press on another
 * issue focuses the one preview tab and hands it the new id, so the column
 * never fills with copies of itself.
 *
 * @throws when the column cannot be prepared, or the shell has no `openTab`.
 */
export function createSidePreviewOpener(input: SidePreviewOpenInput): SidePreviewOpener {
  return async (issueId: string): Promise<void> => {
    const { sidebarRight, folder } = input;
    const openTab = sidebarRight.openTab;
    if (typeof openTab !== "function") {
      throw new Error("this dsh build cannot host the side preview");
    }
    if (folder === null) throw new Error("this view names no project folder");
    await prepareHostedColumn(input, folder);
    openTab.call(sidebarRight, SIDE_PREVIEW_KIND, {
      params: { issueId },
      revealIfOpened: true,
    });
  };
}

/** Whether this host can show the preview at all (seam present). */
export function canOpenSidePreview(
  sidebarRight: SidebarRightLike | null,
  tabTypesRegistered: boolean,
): boolean {
  return tabTypesRegistered && typeof sidebarRight?.openTab === "function";
}
