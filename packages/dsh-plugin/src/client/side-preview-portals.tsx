"use client";

import { useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import { AgentSideStack, IssueSideStack, RuntimeSideStack, SkillSideStack, useCanonicalIssue, useWorkspaceId } from "../fork";
import {
  getSidePreviewTargets,
  subscribeSidePreview,
} from "./side-preview-store";
import type { SidePreviewTargetRef } from "./side-preview";

/**
 * The island's half of the side preview (M-336, generalized in G6).
 *
 * Every dsh tab body of the preview kind publishes its container here, and the
 * island paints the matching side stack into it. The portal keeps the island's
 * providers — the query client, the workspace pointer, the fork's contexts —
 * around content that lives in another React tree (the shell's), which a second
 * root would not have: the preview is the same composition the detail page's
 * sidebar renders, so it must resolve the same queries and mutations.
 *
 * The id and view a stack needs come from the tab's own navigation params,
 * which can change while the tab stays open (a reveal hands the same tab
 * another target). React keys the portal by container, not by target, so a
 * reveal swaps the content of one pane instead of remounting it.
 */
export function SidePreviewPortals() {
  const targets = useSyncExternalStore(subscribeSidePreview, getSidePreviewTargets, getSidePreviewTargets);
  return (
    <>
      {targets.map((target) =>
        createPortal(
          <div className="flex min-h-0 flex-col" data-multica-side-preview-content="">
            {target.target === null ? (
              <SidePreviewEmpty />
            ) : (
              <SidePreviewContent key={`${target.target.view}:${target.target.id}`} target={target.target} />
            )}
          </div>,
          target.surface,
          target.key,
        ),
      )}
    </>
  );
}

/** Dispatches one preview target to its view-specific side stack. */
function SidePreviewContent({ target }: { target: SidePreviewTargetRef }) {
  if (target.view === "issue") {
    return <SidePreviewIssue issueId={target.id} />;
  }
  if (target.view === "agent") {
    return <AgentSideStack agentId={target.id} />;
  }
  if (target.view === "runtime") {
    return <RuntimeSideStack runtimeId={target.id} />;
  }
  if (target.view === "skill") {
    return <SkillSideStack skillId={target.id} />;
  }
  // G6: remaining views wired progressively.
  return <SidePreviewPending target={target} />;
}

/**
 * One issue preview: the identifier header over the fork's side stack.
 *
 * The tab chip is generic because dsh captures it once at open time and never
 * rewrites it, and one tab serves every target a reveal hands it — the
 * identifier therefore lives here, where it follows the params. The read is the
 * same detail query the side stack resolves, so the header costs no second
 * request.
 */
function SidePreviewIssue({ issueId }: { issueId: string }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <SidePreviewIssueHeader issueId={issueId} />
      <IssueSideStack issueId={issueId} />
    </div>
  );
}

/** The identifier and title above the stack; nothing renders until the issue reads. */
function SidePreviewIssueHeader({ issueId }: { issueId: string }) {
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

function labelForView(view: SidePreviewTargetRef["view"]): string {
  switch (view) {
    case "agent":
      return "Agent";
    case "runtime":
      return "Runtime";
    case "skill":
      return "Skill";
    case "project":
      return "Project";
    default:
      return "Preview";
  }
}

/**
 * A preview target whose real side stack is not wired yet. The body header
 * keeps the identifier position consistent with the issue preview.
 */
function SidePreviewPending({ target }: { target: SidePreviewTargetRef }) {
  return (
    <div className="flex h-full flex-col">
      <div
        className="flex items-baseline gap-2 border-b border-border/60 px-3 py-2"
        data-multica-side-preview-header=""
      >
        <span className="shrink-0 text-caption font-medium text-foreground">{labelForView(target.view)}</span>
      </div>
      <div className="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center">
        <p className="text-body font-medium text-foreground">Preview unavailable</p>
        <p className="max-w-xs text-caption text-muted-foreground">
          This side view is not wired to the dsh preview yet.
        </p>
      </div>
    </div>
  );
}

/**
 * A preview tab with no target: a restored layout, or a tab dsh opened without
 * params. Says what it is instead of rendering an empty column.
 */
function SidePreviewEmpty() {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-6 text-center">
      <p className="text-body font-medium text-foreground">No item open</p>
      <p className="max-w-xs text-caption text-muted-foreground">
        Open an item with Side preview and it shows here.
      </p>
    </div>
  );
}
