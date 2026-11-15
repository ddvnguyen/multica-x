"use client";

import { useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import { IssueSideStack, useCanonicalIssue, useWorkspaceId } from "../fork";
import {
  getSidePreviewTargets,
  subscribeSidePreview,
} from "./side-preview-store";
import type { SidePreviewTargetRef } from "./side-preview";

/**
 * The island's half of the side preview (M-336, generalized in G6).
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

function SidePreviewContent({ target }: { target: SidePreviewTargetRef }) {
  if (target.view === "issue") {
    return <SidePreviewIssue issueId={target.id} />;
  }
  return <SidePreviewPending target={target} />;
}

function SidePreviewIssue({ issueId }: { issueId: string }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <SidePreviewIssueHeader issueId={issueId} />
      <IssueSideStack issueId={issueId} />
    </div>
  );
}

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
