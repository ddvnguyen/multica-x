"use client";

import { useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import { IssueSideStack, useCanonicalIssue, useWorkspaceId } from "../fork";
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
              <SidePreviewIssue key={target.issueId} issueId={target.issueId} />
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
 * One preview tab's content: the identifier header over the fork's side stack.
 *
 * The tab chip is generic (`Issue`) because dsh captures it once at open time
 * and never rewrites it, and one tab serves every issue a reveal hands it —
 * the identifier therefore lives here, where it follows the params. The read
 * is the same detail query the side stack resolves, so the header costs no
 * second request.
 */
function SidePreviewIssue({ issueId }: { issueId: string }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <SidePreviewHeader issueId={issueId} />
      <IssueSideStack issueId={issueId} />
    </div>
  );
}

/** The identifier and title above the stack; nothing renders until the issue reads. */
function SidePreviewHeader({ issueId }: { issueId: string }) {
  const wsId = useWorkspaceId();
  const { issue } = useCanonicalIssue(wsId, issueId, { enabled: wsId !== "" });
  if (issue === undefined) return null;
  return (
    <div
      className="flex items-baseline gap-2 border-b border-border/60 px-3 py-2"
      data-multica-side-preview-header=""
      title={issue.title}
    >
      <span className="shrink-0 text-caption font-medium text-foreground">{issue.identifier}</span>
      <span className="truncate text-caption text-muted-foreground">{issue.title}</span>
    </div>
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
