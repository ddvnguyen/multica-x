"use client";

import { useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import { IssueSideStack } from "../fork";
import {
  getSidePreviewTargets,
  subscribeSidePreview,
} from "./side-preview-store";

/**
 * The island's half of the side preview (M-336).
 *
 * Every dsh tab body of the preview kind publishes its container here, and the
 * island paints the issue's side stack into it. The portal keeps the island's
 * providers — the query client, the workspace pointer, the fork's contexts —
 * around content that lives in another React tree (the shell's), which a second
 * root would not have: the preview is the same component the issue page's
 * sidebar renders, so it must resolve the same queries and mutations.
 *
 * `IssueSideStack` is the fork's; the id it needs comes from the tab's own
 * navigation params, which can change while the tab stays open (a reveal hands
 * the same tab another issue). React keys the portal by container, not by
 * issue, so a reveal swaps the content of one pane instead of remounting it.
 */
export function SidePreviewPortals() {
  const targets = useSyncExternalStore(subscribeSidePreview, getSidePreviewTargets, getSidePreviewTargets);
  return (
    <>
      {targets.map((target) =>
        createPortal(
          <div className="flex min-h-0 flex-col" data-multica-side-preview-content="">
            {target.issueId === null ? (
              <SidePreviewEmpty />
            ) : (
              <IssueSideStack key={target.issueId} issueId={target.issueId} />
            )}
          </div>,
          target.surface,
          target.key,
        ),
      )}
    </>
  );
}

/**
 * A preview tab with no issue: a restored layout, or a tab dsh opened without
 * params. Says what it is instead of rendering an empty column.
 */
function SidePreviewEmpty() {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-6 text-center">
      <p className="text-body font-medium text-foreground">No issue open</p>
      <p className="max-w-xs text-caption text-muted-foreground">
        Open an issue with Side preview and it shows here.
      </p>
    </div>
  );
}
