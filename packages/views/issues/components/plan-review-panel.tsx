"use client";

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  isPlanVerdictable,
  planVerdictBody,
  type AgentPlan,
  type AskPlanSection,
  type PlanComment,
  type PlanRevisionRead,
  type PlanSectionVersion,
  type PlanVerdict,
} from "@multica/core/issues/ask-plan";
import {
  useAddPlanComment,
  useResolvePlanComment,
  useReviewPlan,
  useWritePlanSection,
} from "@multica/core/issues/mutations";
import { useAskDraftStore } from "@multica/core/issues/stores/ask-draft-store";
import { issueKeys, planRevisionOptions } from "@multica/core/issues/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useActorName } from "@multica/core/workspace/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { ClipboardList, Loader2, Plus, X } from "@multica/ui/icons";
import { ReadonlyContent } from "../../editor";
import { useT, useTimeAgo } from "../../i18n";
import { AskFreeText, AskSubmitHint } from "./ask-free-text";
import {
  commentThreads,
  docQuote,
  isPlanCommentPending,
  isPlanCommentResolved,
  PLAN_COMMENT_HIGHLIGHT,
  quoteRange,
} from "../utils/plan-doc";
import { PlanSectionEditor } from "./plan-section-editor";
import { PlanSectionHistory } from "./plan-section-history";

/**
 * The plan page (M-275): the Claude Code plan view, ported behaviour for
 * behaviour from the team-room Doc page and restyled with MMX tokens.
 *
 * A member selects a passage inside a section and comments on that passage;
 * the quote is stored and the passage painted with the CSS Custom Highlight
 * API (no DOM rewrite), replies thread under the comment, a thread can be
 * resolved, and a section can be edited or read back through its history. The
 * comments stay pending until a verdict sends them to the agent — that is the
 * same exchange S1-A1 already had, now anchored.
 *
 * Every write is fenced by the plan revision on screen, so a plan the agent
 * moved meanwhile answers a stale revision instead of writing blind.
 *
 * Rendered from both surfaces that review a plan: the issue panel and the
 * Decisions detail pane.
 */

// A text pick over the plan body, resolved from the panel's own root. The
// dsh plugin renders the plan inside a shadow root: after a mouse drag
// there, document.getSelection() is collapsed with its anchor retargeted
// to the host, while the shadow root holds the real range — so the
// document selection is the wrong source inside a shadow tree.
type PlanBodyPick = {
  anchorNode: Node | null;
  focusNode: Node | null;
  text: string;
  rect: { left: number; top: number };
};

// The selection capabilities differ by engine (Chromium has both;
// ShadowRoot.getSelection is newer than getComposedRanges), so each use is
// guarded by a shape check rather than assumed from the lib types.
type ShadowSelectionProvider = { getSelection: () => Selection | null };
type ComposedRangeProvider = {
  getComposedRanges: (options: { shadowRoots: ShadowRoot[] }) => StaticRange[];
};
const asShadowSelectionProvider = (value: object): ShadowSelectionProvider | null => {
  if (!("getSelection" in value)) return null;
  const candidate = value.getSelection;
  return typeof candidate === "function" ? (value as ShadowSelectionProvider) : null;
};
const asComposedRangeProvider = (value: object): ComposedRangeProvider | null => {
  if (!("getComposedRanges" in value)) return null;
  const candidate = value.getComposedRanges;
  return typeof candidate === "function" ? (value as ComposedRangeProvider) : null;
};

// Resolve the pick over the plan body from the panel's own root. Prefers the
// shadow root's selection inside a shadow tree (Chromium), falls back to the
// document selection's ranges composed through the tree where the root has
// no getSelection, and to the document selection outside one.
function planBodyPick(body: HTMLElement): PlanBodyPick | null {
  if (typeof document === "undefined") return null;
  const root = body.getRootNode();
  const shadow =
    typeof ShadowRoot !== "undefined" && root instanceof ShadowRoot ? root : null;
  const provider = shadow === null ? null : asShadowSelectionProvider(shadow);
  const live = provider === null ? document.getSelection() : provider.getSelection();
  if (live !== null && !live.isCollapsed && live.rangeCount > 0) {
    const atRange = live.getRangeAt(0);
    return {
      anchorNode: live.anchorNode,
      focusNode: live.focusNode,
      text: live.toString(),
      // jsdom (and collapsed-then-restored picks) has no rect: park at the
      // viewport origin instead of throwing.
      rect:
        typeof atRange.getBoundingClientRect === "function"
          ? atRange.getBoundingClientRect()
          : { left: 8, top: 48 },
    };
  }
  // No getSelection on the root (older engines): ask the document selection
  // for the ranges it holds inside this shadow tree, and re-anchor from the
  // first one. Direction is unknowable from a static range, but the caller
  // only needs both ends in the same section.
  const doc = document.getSelection();
  const composer = shadow === null || doc === null ? null : asComposedRangeProvider(doc);
  if (shadow !== null && doc !== null && composer !== null) {
    const [first] = composer.getComposedRanges({ shadowRoots: [shadow] });
    if (first !== undefined) {
      const revived = document.createRange();
      revived.setStart(first.startContainer, first.startOffset);
      revived.setEnd(first.endContainer, first.endOffset);
      if (!revived.collapsed) {
        return {
          anchorNode: first.startContainer,
          focusNode: first.endContainer,
          text: revived.toString(),
          rect:
            typeof revived.getBoundingClientRect === "function"
              ? revived.getBoundingClientRect()
              : { left: 8, top: 48 },
        };
      }
    }
  }
  return null;
}

/**
 * The plan page (M-275): the Claude Code plan view, ported behaviour for
 * behaviour from the team-room Doc page and restyled with MMX tokens.
 *
 * A member selects a passage inside a section and comments on that passage;
 * the quote is stored and the passage painted with the CSS Custom Highlight
 * API (no DOM rewrite), replies thread under the comment, a thread can be
 * resolved, and a section can be edited or read back through its history. The
 * comments stay pending until a verdict sends them to the agent — that is the
 * same exchange S1-A1 already had, now anchored.
 *
 * Every write is fenced by the plan revision on screen, so a plan the agent
 * moved meanwhile answers a stale revision instead of writing blind.
 *
 * Rendered from both surfaces that review a plan: the issue panel and the
 * Decisions detail pane.
 */
export function PlanReviewPanel({
  plan,
  issueId,
  onRecorded,
  onRejected,
}: {
  plan: AgentPlan;
  issueId: string;
  onRecorded?: () => void;
  onRejected?: () => void;
}) {
  const { t } = useT("issues");
  const timeAgo = useTimeAgo();
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const review = useReviewPlan(issueId);
  const addComment = useAddPlanComment(issueId);
  const resolveComment = useResolvePlanComment(issueId);
  const writeSection = useWritePlanSection(issueId);
  const { getActorName, hasActor } = useActorName();

  // The floating tooltip: a selection inside one section, with the viewport
  // position it was picked at. Opening the composer keeps the anchor while
  // the tooltip (which the composer replaces) goes away.
  const [tooltip, setTooltip] = useState<
    { sectionId: string; quote: string; x: number; y: number } | undefined
  >();
  const [composer, setComposer] = useState<
    { sectionId: string; quote: string; x: number; y: number } | undefined
  >();
  const [commentText, setCommentText] = useState("");
  const [replyTo, setReplyTo] = useState<PlanComment | null>(null);
  const [replyText, setReplyText] = useState("");
  const planDraftKey = `plan:${plan.id}` as const;
  // Seed from the persisted draft when one exists (navigation, reload,
  // island detach/attach) — same flash-avoidance as the ask card.
  const [comments, setComments] = useState<readonly string[]>(() => {
    const stored = useAskDraftStore.getState().getDraft(planDraftKey);
    return stored?.planComments ?? [];
  });
  const [draft, setDraft] = useState(() => {
    const stored = useAskDraftStore.getState().getDraft(planDraftKey);
    return stored?.planNote ?? "";
  });
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  // Cancelling frees the issue for a new plan and is not undoable, so the
  // button has to be pressed twice — armed, then taken.
  const [cancelArmed, setCancelArmed] = useState(false);
  const [editing, setEditing] = useState<{ sectionId: string; baseMarkdown: string }>();
  const [historyFor, setHistoryFor] = useState<string>();
  const [showResolved, setShowResolved] = useState(false);
  // An older revision under review: undefined is the current plan.
  const [viewedRevision, setViewedRevision] = useState<number>();
  // The thread list sits below the plan, closed until the member opens it.
  const [commentsOpen, setCommentsOpen] = useState(false);

  const bodyRef = useRef<HTMLDivElement>(null);

  const planComments = useMemo(() => plan.comments ?? [], [plan.comments]);
  const threads = useMemo(() => commentThreads(planComments), [planComments]);
  const pendingCount = planComments.filter(
    (comment) => isPlanCommentPending(comment) && !isPlanCommentResolved(comment),
  ).length;
  const resolvedCount = threads.filter(({ root }) => isPlanCommentResolved(root)).length;
  const verdictable = isPlanVerdictable(plan);
  const final = plan.status === "approved" || plan.status === "cancelled";
  const viewingOld = viewedRevision !== undefined && viewedRevision !== plan.revision;
  // Old revisions and final plans are read-only: no edits, no comments, no
  // verdicts. The switcher, the banner and the thread list stay.
  const readOnly = final || viewingOld;
  const busy =
    review.isPending ||
    addComment.isPending ||
    resolveComment.isPending ||
    writeSection.isPending;

  const sectionHeading = useCallback(
    (sectionId: string): string =>
      plan.sections.find((section) => section.sectionId === sectionId)?.heading ??
      sectionId,
    [plan.sections],
  );

  const authorLabel = useCallback(
    (userId: string): string =>
      userId !== "" && hasActor("member", userId) === true
        ? getActorName("member", userId)
        : t(($) => $.ask_plan.unknown_author),
    [getActorName, hasActor, t],
  );

  // A selection inside one current-revision section raises the comment
  // tooltip, parked at the selection's viewport rect. A collapsed or
  // cross-section selection leaves the tooltip alone; read-only views (an
  // older revision, a final plan) never raise it.
  useEffect(() => {
    if (readOnly) {
      setTooltip(undefined);
      return;
    }
    const capture = (): void => {
      const body = bodyRef.current;
      if (body === null) return;
      const pick = planBodyPick(body);
      if (pick === null) return;
      const sectionOf = (node: Node | null): HTMLElement | null => {
        const element =
          node instanceof HTMLElement ? node : (node?.parentElement ?? null);
        const section =
          element?.closest<HTMLElement>("[data-plan-section]") ?? null;
        return section !== null && body.contains(section) ? section : null;
      };
      const anchor = sectionOf(pick.anchorNode);
      if (anchor === null || anchor !== sectionOf(pick.focusNode)) return;
      // Text picked inside the section editor is a draft, not a passage to
      // comment on.
      const picked =
        pick.anchorNode instanceof HTMLElement
          ? pick.anchorNode
          : (pick.anchorNode?.parentElement ?? null);
      if (picked?.closest("[data-plan-section-editor]") != null) return;
      const quote = docQuote(pick.text);
      const sectionId = anchor.dataset["planSection"];
      if (quote === "" || sectionId === undefined) return;
      // An open composer owns its quote: typing in it collapses the page
      // selection, which must not re-raise the tooltip over it.
      if (composer !== undefined) return;
      const x = Math.min(Math.max(8, pick.rect.left), Math.max(8, window.innerWidth - 120));
      const y = Math.max(8, pick.rect.top - 40);
      setTooltip({ sectionId, quote, x, y });
    };
    document.addEventListener("selectionchange", capture);
    // A shadow root fires its own selectionchange for picks inside it; the
    // document listener alone never sees them.
    const root = bodyRef.current?.getRootNode();
    const shadow =
      typeof ShadowRoot !== "undefined" && root instanceof ShadowRoot ? root : null;
    shadow?.addEventListener("selectionchange", capture);
    return () => {
      document.removeEventListener("selectionchange", capture);
      shadow?.removeEventListener("selectionchange", capture);
    };
  }, [composer, readOnly]);

  // The tooltip parks at the picking point; scrolling the plan away from
  // under it dismisses it, like the annotation bubble it mirrors.
  useEffect(() => {
    if (tooltip === undefined) return;
    const dismiss = (): void => setTooltip(undefined);
    window.addEventListener("scroll", dismiss, true);
    return () => {
      window.removeEventListener("scroll", dismiss, true);
    };
  }, [tooltip]);

  // Paint each open comment's passage where the text still holds it. The
  // highlight is a CSS Custom Highlight over the rendered Markdown, so the
  // Markdown DOM itself is never rewritten; browsers without the API skip it
  // and the thread list still shows every quote. On an older revision only
  // that revision's threads (and unlabelled ones, which predate the label)
  // paint; the thread list below always shows them all.
  const highlightRoots = useMemo(
    () =>
      planComments.filter(
        (comment) =>
          (comment.parent_comment_id ?? null) === null &&
          !isPlanCommentResolved(comment) &&
          comment.quote !== "" &&
          (viewingOld !== true ||
            comment.revision == null ||
            comment.revision === viewedRevision),
      ),
    [planComments, viewedRevision, viewingOld],
  );
  useEffect(() => {
    const body = bodyRef.current;
    const registry =
      typeof CSS === "undefined"
        ? undefined
        : (CSS as { highlights?: Map<string, unknown> }).highlights;
    const HighlightType = (
      globalThis as { Highlight?: new (...ranges: Range[]) => unknown }
    ).Highlight;
    if (body === null || registry === undefined || HighlightType === undefined) return;
    const escaped = (value: string): string =>
      typeof CSS !== "undefined" && typeof CSS.escape === "function"
        ? CSS.escape(value)
        : value.replace(/["\\]/g, "\\$&");
    const ranges = highlightRoots.flatMap((comment) => {
      const section = body.querySelector(
        `[data-plan-section="${escaped(comment.section_id)}"] [data-plan-section-body]`,
      );
      const range = section === null ? undefined : quoteRange(section, comment.quote);
      return range === undefined ? [] : [range];
    });
    registry.set(PLAN_COMMENT_HIGHLIGHT, new HighlightType(...ranges));
    return () => {
      registry.delete(PLAN_COMMENT_HIGHLIGHT);
    };
  }, [highlightRoots, editing]);

  // A click on a painted passage opens its thread: the point is hit-tested
  // against each painted quote's client rects, and the thread list opens on
  // the match. CSS highlights are not DOM nodes, so there is nothing else to
  // click — and rects (unlike the caret APIs) pierce shadow roots.
  const openThreadAtPoint = (clientX: number, clientY: number): boolean => {
    const body = bodyRef.current;
    if (body === null) return false;
    const hit = highlightRoots.find((comment) => {
      const section = body.querySelector(
        `[data-plan-section="${CSS.escape(comment.section_id)}"] [data-plan-section-body]`,
      );
      const range = section === null ? undefined : quoteRange(section, comment.quote);
      if (range === undefined || typeof range.getClientRects !== "function") return false;
      return Array.from(range.getClientRects()).some(
        (rect) =>
          clientX >= rect.left &&
          clientX <= rect.right &&
          clientY >= rect.top &&
          clientY <= rect.bottom,
      );
    });
    if (hit === undefined) return false;
    setCommentsOpen(true);
    setReplyTo(null);
    requestAnimationFrame(() => {
      // The thread list lives in the panel's own tree, which may be a shadow
      // root — query it, not the document.
      const root = body.getRootNode();
      const scoped =
        root instanceof Document || root instanceof ShadowRoot
          ? root.querySelector(`[data-plan-comment="${hit.id}"]`)
          : null;
      scoped?.scrollIntoView({ block: "nearest" });
    });
    return true;
  };

  const handleWriteError = useCallback(
    (cause: unknown): void => {
      const status = (cause as { status?: unknown } | null)?.status;
      if (status === 409) {
        // A 409 is the plan moving under us — closed, or on another revision.
        // Re-read as well as reporting, which puts the current state back in
        // front of the member.
        setNotice(t(($) => $.ask_plan.stale_notice));
        void queryClient.invalidateQueries({ queryKey: issueKeys.askPlan(wsId, issueId) });
        onRejected?.();
        return;
      }
      setError(
        cause instanceof Error && cause.message !== ""
          ? cause.message
          : t(($) => $.ask_plan.failed),
      );
    },
    [issueId, onRejected, queryClient, t, wsId],
  );

  const clearComposer = (): void => {
    setTooltip(undefined);
    setComposer(undefined);
    setCommentText("");
    // Clear the panel's own tree: inside a shadow root the document
    // selection is already collapsed, and only the root's own selection
    // holds the pick.
    const body = bodyRef.current;
    if (typeof document === "undefined" || body === null) return;
    const root = body.getRootNode();
    const shadow =
      typeof ShadowRoot !== "undefined" && root instanceof ShadowRoot ? root : null;
    const provider = shadow === null ? null : asShadowSelectionProvider(shadow);
    const selection = provider === null ? document.getSelection() : provider.getSelection();
    selection?.removeAllRanges();
  };

  const submitComment = (): void => {
    if (composer === undefined || busy) return;
    const body = commentText.trim();
    if (body === "") return;
    setError(null);
    setNotice(null);
    addComment.mutate(
      {
        planId: plan.id,
        comment: {
          base_revision: plan.revision,
          section_id: composer.sectionId,
          quote: composer.quote,
          body,
        },
      },
      {
        onSuccess: () => {
          clearComposer();
          // The new thread lands in the list below: open it so the member
          // sees the highlight and the saved comment together.
          setCommentsOpen(true);
        },
        onError: handleWriteError,
      },
    );
  };

  const submitReply = (): void => {
    if (replyTo === null || busy) return;
    const body = replyText.trim();
    if (body === "") return;
    setError(null);
    setNotice(null);
    addComment.mutate(
      {
        planId: plan.id,
        comment: {
          base_revision: plan.revision,
          section_id: replyTo.section_id,
          quote: replyTo.quote,
          body,
          parent_comment_id: replyTo.id,
        },
      },
      {
        onSuccess: () => {
          setReplyText("");
          setReplyTo(null);
        },
        onError: handleWriteError,
      },
    );
  };

  const resolveThread = (comment: PlanComment): void => {
    if (busy) return;
    setError(null);
    setNotice(null);
    resolveComment.mutate(
      { planId: plan.id, commentId: comment.id },
      {
        onError: (cause) => {
          // 409 `comment_resolved`: someone else already resolved it, which is
          // a re-read, not a failure to report as one.
          if ((cause as { status?: unknown } | null)?.status === 409) {
            void queryClient.invalidateQueries({
              queryKey: issueKeys.askPlan(wsId, issueId),
            });
            return;
          }
          handleWriteError(cause);
        },
      },
    );
  };

  // The older revision under view, if any. Keyed per revision, so each one
  // fetches once and stays cached; disabled on the current revision, which
  // the island read already carries.
  const revisionRead = useQuery({
    ...planRevisionOptions(wsId, plan.id, viewedRevision ?? 0),
    enabled: viewingOld === true && !!wsId && (viewedRevision ?? 0) > 0,
  });
  const oldRevision: PlanRevisionRead | undefined = revisionRead.data;

  const saveSection = (
    sectionId: string,
    heading: string,
    markdown: string,
    closeEditor: boolean,
  ): void => {
    if (busy) return;
    setError(null);
    setNotice(null);
    writeSection.mutate(
      {
        planId: plan.id,
        sectionId,
        body: { base_revision: plan.revision, heading, markdown },
      },
      {
        // A stale save keeps the editor open: the re-read puts the newer text
        // above the draft, so the second save is a knowing overwrite.
        onSuccess: () => {
          if (closeEditor) setEditing(undefined);
        },
        onError: handleWriteError,
      },
    );
  };

  const restoreVersion = (sectionId: string, version: PlanSectionVersion): void => {
    saveSection(sectionId, version.heading, version.markdown, false);
  };

  const record = (verdict: PlanVerdict): void => {
    if (busy) return;
    setError(null);
    setNotice(null);
    setCancelArmed(false);
    review.mutate(
      { planId: plan.id, verdict: planVerdictBody(plan, verdict, comments) },
      {
        onSuccess: () => {
          // The verdict is recorded: the staged comments are spent.
          useAskDraftStore.getState().clearDraft(planDraftKey);
          setComments([]);
          setDraft("");
          onRecorded?.();
        },
        onError: (cause) => {
          onRejected?.();
          setError(
            cause instanceof Error && cause.message !== ""
              ? cause.message
              : t(($) => $.ask_plan.failed),
          );
        },
      },
    );
  };

  // Persist the staged comments + the in-progress line for restore after
  // navigation, reload, or island detach/attach. Runs on this panel's own
  // state rather than the store subscription so typing never loops.
  useEffect(() => {
    const store = useAskDraftStore.getState();
    if (comments.length === 0 && draft.trim() === "") {
      if (store.getDraft(planDraftKey) !== undefined) store.clearDraft(planDraftKey);
      return;
    }
    store.setPlanComments(planDraftKey, issueId, comments);
    if (draft !== (store.getDraft(planDraftKey)?.planNote ?? "")) {
      store.setPlanNote(planDraftKey, issueId, draft);
    }
  }, [comments, draft, planDraftKey, issueId]);

  const addFlatComment = (): void => {
    const text = draft.trim();
    if (text === "") return;
    setComments((current) => [...current, text]);
    setDraft("");
  };

  const commentRow = (comment: PlanComment, reply: boolean) => {
    const resolved = isPlanCommentResolved(comment);
    return (
      <li
        key={comment.id}
        data-plan-comment={comment.id}
        className={
          reply
            ? "border-l-2 border-border pl-2"
            : undefined
        }
      >
        <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
          <span className="text-caption font-medium text-foreground">
            {authorLabel(comment.author_user_id)}
          </span>
          {!reply && (
            <span className="truncate text-caption text-muted-foreground">
              {sectionHeading(comment.section_id)}
            </span>
          )}
          {/* M-348: the revision the thread was made on. Unlabelled threads
              predate the label and show no badge. */}
          {!reply && comment.revision != null && (
            <span className="rounded-full border border-border px-1.5 text-caption text-muted-foreground">
              {t(($) => $.ask_plan.comment_revision, { revision: comment.revision })}
            </span>
          )}
          {!reply && resolved ? (
            <span className="ml-auto rounded-full px-1.5 text-caption text-muted-foreground">
              {t(($) => $.ask_plan.resolved_badge)}
            </span>
          ) : !reply && isPlanCommentPending(comment) ? (
            <span className="ml-auto rounded-full border border-border px-1.5 text-caption text-foreground">
              {t(($) => $.ask_plan.pending_badge)}
            </span>
          ) : !reply ? (
            <span className="ml-auto rounded-full px-1.5 text-caption text-muted-foreground">
              {t(($) => $.ask_plan.delivered_badge)}
            </span>
          ) : null}
        </div>
        {comment.quote !== "" && (
          <blockquote className="my-1 border-l-2 border-border pl-2 text-caption text-muted-foreground">
            {comment.quote}
          </blockquote>
        )}
        <p className="whitespace-pre-wrap text-caption text-foreground">
          {comment.body}
        </p>
        {!reply && (
          <div className="mt-1 flex flex-wrap items-center gap-2">
            {!resolved && !readOnly && (
              <Button
                size="xs"
                variant="ghost"
                disabled={busy}
                onClick={() => resolveThread(comment)}
              >
                {t(($) => $.ask_plan.resolve)}
              </Button>
            )}
            {!readOnly && (
              <Button
                size="xs"
                variant="ghost"
                disabled={busy}
                aria-expanded={replyTo?.id === comment.id}
                onClick={() =>
                  setReplyTo((current) => (current?.id === comment.id ? null : comment))
                }
              >
                {t(($) => $.ask_plan.reply)}
              </Button>
            )}
            {comment.created_at !== "" && (
              <span className="text-caption text-muted-foreground">
                {timeAgo(comment.created_at)}
              </span>
            )}
          </div>
        )}
        {!reply && !readOnly && replyTo?.id === comment.id && (
          <form
            className="mt-1.5 flex items-start gap-1.5"
            onSubmit={(event) => {
              event.preventDefault();
              submitReply();
            }}
          >
            <div className="min-w-0 flex-1">
              <AskFreeText
                id={`${planDraftKey}-reply:${replyTo?.id ?? "new"}`}
                autoFocus
                value={replyText}
                max={4000}
                showCount
                aria-label={t(($) => $.ask_plan.reply_placeholder)}
                placeholder={t(($) => $.ask_plan.reply_placeholder)}
                disabled={busy}
                onChange={(event) => setReplyText(event.currentTarget.value)}
                onSubmit={submitReply}
                onKeyDown={(event) => {
                  // The reply is a one-line row: plain Enter sends, matching
                  // the Input it replaced. Multi-line replies go through the
                  // button (or paste), where Cmd/Ctrl+Enter still applies.
                  if (event.key !== "Enter" || event.nativeEvent.isComposing) return;
                  if (event.metaKey || event.ctrlKey || !event.shiftKey) {
                    event.preventDefault();
                    submitReply();
                  }
                }}
              />
            </div>
            <Button size="xs" variant="outline" type="submit" disabled={replyText.trim() === ""}>
              {t(($) => $.ask_plan.reply_send)}
            </Button>
          </form>
        )}
      </li>
    );
  };

  const visibleThreads = threads.filter(
    ({ root }) => showResolved || !isPlanCommentResolved(root),
  );

  return (
    <section
      aria-label={plan.title}
      data-agent-plan={plan.status}
      data-plan-review={plan.id}
      className="rounded-md border border-border bg-muted/40 px-3 py-2.5"
    >
      <div className="flex flex-wrap items-start gap-x-2 gap-y-1">
        <p className="flex min-w-0 items-center gap-1.5 text-caption font-medium text-foreground">
          <ClipboardList aria-hidden className="size-3.5 shrink-0 text-info" />
          <span className="min-w-0 break-words">{plan.title}</span>
        </p>
        {/* M-348: the revision switcher. One revision means nothing to pick,
            so the label stays plain text; more earns a dropdown. */}
        <span className="ml-auto shrink-0 text-caption text-muted-foreground">
          {plan.revision <= 1 ? (
            t(($) => $.ask_plan.revision, { revision: plan.revision })
          ) : (
            <label className="inline-flex items-center gap-1">
              <span className="sr-only">{t(($) => $.ask_plan.revision_switcher)}</span>
              <select
                aria-label={t(($) => $.ask_plan.revision_switcher)}
                value={viewingOld === true ? viewedRevision : plan.revision}
                disabled={busy}
                onChange={(event) => {
                  const picked = Number(event.currentTarget.value);
                  setViewedRevision(picked === plan.revision ? undefined : picked);
                  setTooltip(undefined);
                  setComposer(undefined);
                  setEditing(undefined);
                  setHistoryFor(undefined);
                }}
                className="rounded-xs border border-border bg-background px-1 py-0.5 text-caption text-foreground outline-none focus-visible:ring-1 focus-visible:ring-ring"
              >
                {Array.from({ length: plan.revision }, (_, index) => index + 1).map(
                  (revision) => (
                    <option key={revision} value={revision}>
                      {t(($) => $.ask_plan.revision, { revision })}
                      {revision === plan.revision
                        ? ` ${t(($) => $.ask_plan.history_current)}`
                        : ""}
                    </option>
                  ),
                )}
              </select>
            </label>
          )}
        </span>
      </div>

      {notice !== null && (
        <p role="status" className="mt-1 text-caption text-warning">
          {notice}
        </p>
      )}

      {/* M-348: an older revision reads read-only under this banner, with one
          action back to the current revision. */}
      {viewingOld === true && (
        <div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 rounded-md border border-border bg-background px-2.5 py-2">
          <p role="status" className="min-w-0 flex-1 text-caption text-muted-foreground">
            {t(($) => $.ask_plan.revision_viewing, {
              revision: viewedRevision,
              current: plan.revision,
            })}
          </p>
          <Button
            size="xs"
            variant="outline"
            onClick={() => {
              setViewedRevision(undefined);
              setTooltip(undefined);
              setComposer(undefined);
            }}
          >
            {t(($) => $.ask_plan.revision_back)}
          </Button>
        </div>
      )}

      {viewingOld === true && revisionRead.isPending && (
        <p role="status" className="mt-2 text-caption text-muted-foreground">
          {t(($) => $.ask_plan.history_loading)}
        </p>
      )}
      {viewingOld === true && revisionRead.isError && (
        <p role="alert" className="mt-2 text-caption text-destructive">
          {t(($) => $.ask_plan.history_failed)}
        </p>
      )}

      <article
        ref={bodyRef}
        aria-label={plan.title}
        className="mt-2 min-w-0"
        onClick={(event) => {
          // A painted passage opens its thread. Only plain left clicks on
          // text count: edits, buttons and links keep their own behavior.
          if (
            event.button !== 0 ||
            event.metaKey ||
            event.ctrlKey ||
            event.altKey ||
            event.shiftKey ||
            (event.target instanceof HTMLElement &&
              event.target.closest("button, a, input, textarea, select"))
          ) {
            return;
          }
          openThreadAtPoint(event.clientX, event.clientY);
        }}
      >
        {viewingOld === true ? (
          <OldRevisionSections
            read={oldRevision}
            revision={viewedRevision ?? plan.revision}
            tRevisionUnavailable={(heading) =>
              t(($) => $.ask_plan.revision_unavailable, {
                revision: viewedRevision,
                heading,
              })
            }
          />
        ) : (
          <>
            {plan.sections.length === 0 && (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.ask_plan.sections_empty)}
              </p>
            )}
            {plan.sections.map((section) => (
              <PlanSection
                key={section.sectionId}
                section={section}
                busy={busy}
                final={readOnly}
                editing={editing?.sectionId === section.sectionId}
                baseMarkdown={editing?.baseMarkdown ?? ""}
                saving={writeSection.isPending}
                historyOpen={historyFor === section.sectionId}
                onEdit={() =>
                  setEditing({ sectionId: section.sectionId, baseMarkdown: section.markdown })
                }
                onSave={(heading, markdown) =>
                  saveSection(section.sectionId, heading, markdown, true)
                }
                onCancelEdit={() => setEditing(undefined)}
                onToggleHistory={() =>
                  setHistoryFor((current) =>
                    current === section.sectionId ? undefined : section.sectionId,
                  )
                }
              />
            ))}
          </>
        )}
      </article>

      {historyFor !== undefined && viewingOld !== true && (
        <div className="mt-2">
          <PlanSectionHistory
            planId={plan.id}
            sectionId={historyFor}
            heading={sectionHeading(historyFor)}
            revision={plan.revision}
            restoring={writeSection.isPending}
            onClose={() => setHistoryFor(undefined)}
            {...(readOnly
              ? {}
              : {
                  onRestore: (version: PlanSectionVersion) =>
                    restoreVersion(historyFor, version),
                })}
          />
        </div>
      )}

      {/* M-348: the selection tooltip. Fixed at the picking point, flipped
          above the selection; scrolling dismisses it (see the scroll effect).
          The composer replaces it, anchored at the same point. */}
      {tooltip !== undefined && composer === undefined && viewingOld !== true && (
        <div
          role="dialog"
          aria-label={t(($) => $.ask_plan.comment_on_passage)}
          className="fixed z-50 w-max max-w-[calc(100vw-16px)] rounded-md border border-border bg-background px-2 py-1.5 shadow-md"
          style={{ left: tooltip.x, top: tooltip.y }}
        >
          <div className="flex items-center gap-1.5">
            <Button
              size="xs"
              variant="default"
              autoFocus
              disabled={busy}
              onClick={() => {
                setComposer({
                  sectionId: tooltip.sectionId,
                  quote: tooltip.quote,
                  x: tooltip.x,
                  y: tooltip.y,
                });
                setTooltip(undefined);
              }}
            >
              {t(($) => $.ask_plan.comment_on_selection)}
            </Button>
            <button
              type="button"
              aria-label={t(($) => $.ask_plan.clear_selection)}
              title={t(($) => $.ask_plan.clear_selection)}
              onClick={clearComposer}
              className="shrink-0 rounded-xs p-0.5 text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring"
            >
              <X aria-hidden className="size-3.5" />
            </button>
          </div>
        </div>
      )}
      {composer !== undefined && viewingOld !== true && (
        <div
          role="dialog"
          aria-label={t(($) => $.ask_plan.comment_on_passage)}
          className="fixed z-50 w-72 max-w-[calc(100vw-16px)] rounded-md border border-border bg-background px-2.5 py-2 shadow-md"
          style={{ left: composer.x, top: composer.y }}
        >
          <form
            data-plan-comment-box={composer.sectionId}
            onSubmit={(event) => {
              event.preventDefault();
              submitComment();
            }}
          >
            <div className="flex items-start gap-1.5">
              <blockquote className="min-w-0 flex-1 border-l-2 border-border pl-2 text-caption text-muted-foreground">
                {t(($) => $.ask_plan.selection_quote, { quote: composer.quote })}
              </blockquote>
              <button
                type="button"
                aria-label={t(($) => $.ask_plan.clear_selection)}
                title={t(($) => $.ask_plan.clear_selection)}
                disabled={busy}
                onClick={clearComposer}
                className="shrink-0 rounded-xs p-0.5 text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring"
              >
                <X aria-hidden className="size-3.5" />
              </button>
            </div>
            <p className="mt-1 text-caption text-muted-foreground">
              {sectionHeading(composer.sectionId)}
            </p>
            <AskFreeText
              id={`${planDraftKey}-passage:${composer.sectionId}`}
              autoFocus
              aria-label={t(($) => $.ask_plan.comment_on_passage)}
              placeholder={t(($) => $.ask_plan.comment_on_passage)}
              value={commentText}
              max={4000}
              showCount
              hint={t(($) => $.ask_plan.comment_shortcut)}
              disabled={busy}
              onChange={(event) => setCommentText(event.currentTarget.value)}
              onSubmit={submitComment}
              onKeyDown={(event) => {
                if (event.key === "Escape") clearComposer();
              }}
            />
            <div className="mt-1 flex items-center justify-end gap-2">
              <Button
                size="xs"
                variant="default"
                type="submit"
                disabled={busy || commentText.trim() === ""}
              >
                {t(($) => $.ask_plan.add_passage_comment)}
              </Button>
            </div>
          </form>
        </div>
      )}
      {/* M-348: the thread list sits below the plan, closed by default, so it
          never narrows the reading column. */}
      <div className="mt-2.5 border-t border-border pt-2">
        <button
          type="button"
          aria-expanded={commentsOpen}
          onClick={() => setCommentsOpen((current) => !current)}
          className="flex w-full items-center gap-1.5 rounded-xs text-caption font-medium text-foreground outline-none focus-visible:ring-1 focus-visible:ring-ring"
        >
          {commentsOpen
            ? t(($) => $.ask_plan.comments_hide)
            : t(($) => $.ask_plan.comments_show, { n: planComments.length })}
        </button>
        {commentsOpen && (
          <div className="mt-1">
            {planComments.length === 0 ? (
              <p className="mt-0.5 text-caption text-muted-foreground">
                {t(($) => $.ask_plan.comments_threads_empty)}
              </p>
            ) : (
              <ul className="mt-1 space-y-2">
                {visibleThreads.map(({ root, replies }) => (
                  <li
                    key={root.id}
                    className={
                      isPlanCommentResolved(root)
                        ? "border-t border-border pt-2 opacity-60"
                        : "border-t border-border pt-2"
                    }
                  >
                    <ul className="space-y-1.5">
                      {commentRow(root, false)}
                      {replies.map((reply) => commentRow(reply, true))}
                    </ul>
                  </li>
                ))}
              </ul>
            )}
            {resolvedCount > 0 && (
              <button
                type="button"
                aria-expanded={showResolved}
                onClick={() => setShowResolved((current) => !current)}
                className="mt-2 rounded-xs text-caption text-muted-foreground outline-none hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring"
              >
                {showResolved
                  ? t(($) => $.ask_plan.hide_resolved)
                  : t(($) => $.ask_plan.show_resolved, { n: resolvedCount })}
              </button>
            )}
          </div>
        )}
      </div>

      {!verdictable || viewingOld === true ? (
        // An older revision shows no verdict row and no draft line: the
        // banner above is its whole status.
        viewingOld !== true && (
          <p className="mt-2.5 text-caption text-muted-foreground">
            {plan.status === "changes_requested"
              ? t(($) => $.ask_plan.plan_changes_requested)
              : t(($) => $.ask_plan.plan_draft)}
          </p>
        )
      ) : (
        <>
          <p className="mt-3 text-caption font-medium text-foreground">
            {t(($) => $.ask_plan.comments_heading)}
          </p>
          {comments.length === 0 ? (
            <p className="mt-0.5 text-caption text-muted-foreground">
              {t(($) => $.ask_plan.comments_empty)}
            </p>
          ) : (
            <ul className="mt-1 space-y-1">
              {comments.map((comment, index) => (
                <li
                  key={`${index}:${comment}`}
                  className="flex items-start gap-1.5 rounded-xs bg-background/70 px-2 py-1.5 text-caption"
                >
                  <span className="min-w-0 flex-1 whitespace-pre-wrap text-foreground">
                    {comment}
                  </span>
                  <button
                    type="button"
                    aria-label={t(($) => $.ask_plan.remove_comment)}
                    onClick={() =>
                      setComments((current) =>
                        current.filter((_, at) => at !== index),
                      )
                    }
                    className="shrink-0 rounded-xs p-0.5 text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring"
                  >
                    <X aria-hidden className="size-3.5" />
                  </button>
                </li>
              ))}
            </ul>
          )}

          <div className="mt-1.5 flex items-start gap-1.5">
            <div className="min-w-0 flex-1">
              <AskFreeText
                id={`${planDraftKey}-note`}
                value={draft}
                max={4000}
                showCount
                hint={t(($) => $.agent_question.multiline_hint)}
                placeholder={t(($) => $.ask_plan.comment_placeholder)}
                aria-label={t(($) => $.ask_plan.comment_placeholder)}
                disabled={busy}
                onChange={(event) => setDraft(event.currentTarget.value)}
                onSubmit={addFlatComment}
              />
            </div>
            <Button
              size="xs"
              variant="outline"
              disabled={draft.trim() === ""}
              onClick={addFlatComment}
            >
              <Plus aria-hidden className="size-3.5" />
              {t(($) => $.ask_plan.add_comment)}
            </Button>
          </div>

          {pendingCount > 0 && (
            <p className="mt-1.5 text-caption text-muted-foreground">
              {t(($) => $.ask_plan.pending_hint, { n: pendingCount })}
            </p>
          )}

          {error !== null && (
            <p role="alert" className="mt-2 text-caption text-destructive">
              {error}
            </p>
          )}

          <div className="mt-2.5 flex flex-wrap items-center justify-end gap-2">
            {error === null && <AskSubmitHint id={`${planDraftKey}-submit`} />}
            <Button
              size="xs"
              variant="outline"
              disabled={busy}
              onClick={() => {
                if (!cancelArmed) {
                  setCancelArmed(true);
                  return;
                }
                record("cancel");
              }}
            >
              {busy && review.variables?.verdict.verdict === "cancel" && (
                <Loader2 aria-hidden className="animate-spin motion-reduce:animate-none" />
              )}
              {cancelArmed
                ? t(($) => $.ask_plan.cancel_confirm)
                : t(($) => $.ask_plan.cancel)}
            </Button>
            <Button
              size="xs"
              variant="outline"
              disabled={busy || (comments.length === 0 && pendingCount === 0)}
              onClick={() => record("changes_requested")}
            >
              {busy && review.variables?.verdict.verdict === "changes_requested" && (
                <Loader2 aria-hidden className="animate-spin motion-reduce:animate-none" />
              )}
              {t(($) => $.agent_question.request_changes)}
            </Button>
            <Button
              size="xs"
              variant="default"
              disabled={busy}
              onClick={() => record("approve")}
            >
              {busy && review.variables?.verdict.verdict === "approve" && (
                <Loader2 aria-hidden className="animate-spin motion-reduce:animate-none" />
              )}
              {t(($) => $.agent_question.approve)}
            </Button>
          </div>
        </>
      )}
    </section>
  );
}

/**
 * One older revision's sections, read-only (M-348). Sections the ledger has
 * no version for at that revision render a placeholder row — the endpoint
 * marks them `available: false` instead of inventing text.
 */
function OldRevisionSections({
  read,
  revision,
  tRevisionUnavailable,
}: {
  read: PlanRevisionRead | undefined;
  revision: number;
  tRevisionUnavailable: (heading: string) => string;
}) {
  const { t } = useT("issues");
  if (read === undefined) return null;
  if (read.sections.length === 0) {
    return (
      <p className="text-caption text-muted-foreground">
        {t(($) => $.ask_plan.sections_empty)}
      </p>
    );
  }
  return (
    <>
      {read.sections.map((section) => (
        <section
          key={section.section_id}
          className="mb-3 last:mb-0"
          data-plan-section={section.section_id}
          aria-labelledby={`plan-section-${section.section_id}-r${revision}`}
        >
          <p
            id={`plan-section-${section.section_id}-r${revision}`}
            className="min-w-0 break-words text-body font-medium text-foreground"
          >
            {section.heading}
          </p>
          {section.available ? (
            section.markdown.trim() === "" ? (
              <p
                data-plan-section-body
                className="mt-0.5 text-caption text-muted-foreground"
              >
                {t(($) => $.ask_plan.section_empty)}
              </p>
            ) : (
              <div data-plan-section-body className="mt-0.5 text-body">
                <ReadonlyContent content={section.markdown} />
              </div>
            )
          ) : (
            <p
              data-plan-section-body
              className="mt-0.5 text-caption text-muted-foreground"
            >
              {tRevisionUnavailable(section.heading)}
            </p>
          )}
        </section>
      ))}
    </>
  );
}

/** One section: heading, its quiet actions, and the rendered Markdown body. */
function PlanSection({
  section,
  busy,
  final,
  editing,
  baseMarkdown,
  saving,
  historyOpen,
  onEdit,
  onSave,
  onCancelEdit,
  onToggleHistory,
}: {
  section: AskPlanSection;
  busy: boolean;
  final: boolean;
  editing: boolean;
  baseMarkdown: string;
  saving: boolean;
  historyOpen: boolean;
  onEdit: () => void;
  onSave: (heading: string, markdown: string) => void;
  onCancelEdit: () => void;
  onToggleHistory: () => void;
}) {
  const { t } = useT("issues");
  return (
    <section
      className="mb-3 last:mb-0"
      data-plan-section={section.sectionId}
      aria-labelledby={`plan-section-${section.sectionId}`}
    >
      <div className="flex items-baseline gap-2">
        <p
          id={`plan-section-${section.sectionId}`}
          className="min-w-0 break-words text-body font-medium text-foreground"
        >
          {section.heading}
        </p>
        <span className="ml-auto flex shrink-0 gap-0.5 opacity-60 transition-opacity focus-within:opacity-100 hover:opacity-100">
          {!final && !editing && (
            <Button
              size="xs"
              variant="ghost"
              disabled={busy}
              aria-label={t(($) => $.ask_plan.edit_section_named, {
                heading: section.heading,
              })}
              onClick={onEdit}
            >
              {t(($) => $.ask_plan.edit_section)}
            </Button>
          )}
          <Button
            size="xs"
            variant="ghost"
            aria-label={t(($) => $.ask_plan.history_named, { heading: section.heading })}
            aria-pressed={historyOpen}
            onClick={onToggleHistory}
          >
            {t(($) => $.ask_plan.history)}
          </Button>
        </span>
      </div>
      {editing ? (
        <div className="mt-1">
          <PlanSectionEditor
            section={section}
            baseMarkdown={baseMarkdown}
            saving={saving}
            save={onSave}
            cancel={onCancelEdit}
          />
        </div>
      ) : section.markdown.trim() === "" ? (
        <p
          data-plan-section-body
          className="mt-0.5 text-caption text-muted-foreground"
        >
          {t(($) => $.ask_plan.section_empty)}
        </p>
      ) : (
        <div data-plan-section-body className="mt-0.5 text-body">
          <ReadonlyContent content={section.markdown} />
        </div>
      )}
    </section>
  );
}
