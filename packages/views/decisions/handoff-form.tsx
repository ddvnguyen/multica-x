"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  handoffDecision,
  parseDecisionBlock,
  readHandoffContext,
  stripDecisionBlock,
  useRespondDecision,
  type Decision,
  type DecisionStatusDetail,
  type HandoffOption,
} from "@multica/core/decisions";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueTimelineOptions } from "@multica/core/issues/queries";
import type { IssueStatus } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { Check, Flag, Loader2 } from "@multica/ui/icons";
import { ReadonlyContent } from "../editor";
import { useT } from "../i18n";
import { useIssueStatusOptions } from "../issues/utils/status-options";

/**
 * A `wait_for_decision` handoff, answered in place (M-274).
 *
 * The question is not in the row: a handoff parks the issue, and what the agent
 * asked is its last comment. So this form reads the issue's timeline for that
 * comment and for the status the issue left to enter the waiting step, and the
 * comment's optional `decision` block for the choices on offer. A handoff
 * written before that convention (or by an agent that did not use it) still
 * answers: the choices fall back to Approve / Request changes / Other and the
 * destination is picked explicitly.
 *
 * Nothing here is optimistic. The server fences the write on the issue's
 * revision and on its still being in the waiting step, so a stale tab is
 * refused rather than silently re-answering; a 409 re-reads the issue and says
 * what happened, and the same submission keeps its `client_request_id` so a
 * dropped response cannot record the decision twice.
 */
export function HandoffForm({
  decision,
  detail,
  onChanged,
}: {
  decision: Decision;
  detail: DecisionStatusDetail;
  onChanged: () => void;
}) {
  const { t } = useT("decisions");
  const wsId = useWorkspaceId();
  const respond = useRespondDecision();
  const timeline = useQuery(issueTimelineOptions(detail.issueId));
  const context = useMemo(
    () => readHandoffContext(timeline.data ?? [], detail.status),
    [timeline.data, detail.status],
  );
  const block = useMemo(
    () =>
      context.comment === null
        ? null
        : parseDecisionBlock(context.comment.content),
    [context.comment],
  );
  const { options: statuses } = useIssueStatusOptions(
    wsId,
    decision.project_id,
    detail.status as IssueStatus,
  );
  // The waiting step is never a destination: the server refuses a
  // `next_status` that does not leave it.
  const destinations = useMemo(
    () => statuses.filter((status) => status.key !== detail.status),
    [statuses, detail.status],
  );

  const choices: HandoffOption[] = useMemo(
    () =>
      block?.options ?? [
        { key: "approve", label: t(($) => $.detail.handoff.option_approve), detail: "", nextStatus: null },
        { key: "request_changes", label: t(($) => $.detail.handoff.option_request_changes), detail: "", nextStatus: null },
        { key: "other", label: t(($) => $.detail.handoff.option_custom), detail: "", nextStatus: null },
      ],
    [block, t],
  );
  const legalDestination = (key: string | null): string | null =>
    key !== null && destinations.some((status) => status.key === key) ? key : null;

  const [picked, setPicked] = useState<string | null>(null);
  const [destination, setDestination] = useState<string | null>(null);
  const [text, setText] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [stale, setStale] = useState(false);
  const [recorded, setRecorded] = useState<{ decision: string; next_status: string } | null>(null);

  const chosen =
    choices.find((choice) => choice.key === (picked ?? block?.defaultKey ?? null)) ?? null;
  // The transition into the waiting step is the honest default: it is where the
  // issue came from. When the timeline does not say, or no longer allows it,
  // the member has to name a destination — guessing one could start an agent.
  const nextStatus = destination ?? legalDestination(context.priorStatus) ?? "";
  const ready = (chosen !== null || text.trim() !== "") && nextStatus !== "";

  const pick = (choice: HandoffOption): void => {
    setPicked(choice.key);
    const target = legalDestination(choice.nextStatus);
    if (target !== null) setDestination(target);
  };

  const submit = async (): Promise<void> => {
    if (!ready || respond.isPending) return;
    const decisionText = handoffDecision(chosen, text);
    if (decisionText === null) return;
    setError(null);
    setStale(false);
    try {
      const receipt = await respond.mutateAsync({
        issueId: detail.issueId,
        expected_issue_revision: decision.issue_revision,
        decision: decisionText,
        rationale: text.trim(),
        next_status: nextStatus,
      });
      setRecorded({ decision: receipt.decision, next_status: receipt.next_status });
      onChanged();
    } catch (cause) {
      // A conflict is either a revision fence or an issue that left the waiting
      // step while this form was open: re-read it and let the member look before
      // answering again. A 400 is the server naming what it refused.
      if (typeof cause === "object" && cause !== null && (cause as { status?: unknown }).status === 409) {
        setStale(true);
        onChanged();
        return;
      }
      setError(
        cause instanceof Error && cause.message !== ""
          ? cause.message
          : t(($) => $.detail.handoff.failed),
      );
    }
  };

  const prose = context.comment === null ? "" : stripDecisionBlock(context.comment.content);
  const labelOf = (key: string): string =>
    statuses.find((status) => status.key === key)?.label ?? key;

  return (
    <section
      data-handoff-form={detail.status}
      aria-label={t(($) => $.detail.status_title)}
      className="mt-2 min-w-0 rounded-md border border-border bg-muted/40 px-3 py-2.5"
    >
      <p className="flex items-center gap-1.5 text-caption font-medium text-foreground">
        <Flag aria-hidden="true" className="size-3.5 shrink-0 text-warning" />
        {t(($) => $.detail.status_title)}
      </p>

      {prose !== "" && (
        <div className="mt-2 max-h-72 overflow-y-auto rounded-xs bg-background/70 px-2.5 py-2 text-caption">
          <ReadonlyContent content={prose} />
        </div>
      )}
      {block !== null && block.question !== "" && (
        <p className="mt-2 text-body text-foreground">{block.question}</p>
      )}

      {timeline.isPending ? (
        // The comment decides what is on offer, so the choices wait for it
        // rather than flashing the fallback set and replacing it.
        <p className="mt-2 flex items-center gap-1.5 text-caption text-muted-foreground">
          <Loader2 aria-hidden="true" className="animate-spin motion-reduce:animate-none" />
          {t(($) => $.detail.loading)}
        </p>
      ) : (
        <fieldset className="mt-2 space-y-1" disabled={respond.isPending || recorded !== null}>
          <legend className="text-caption text-muted-foreground">
            {t(($) => $.detail.handoff.answer_legend)}
          </legend>
          {choices.map((choice) => {
            const selectedChoice = choice.key === chosen?.key;
            return (
              <label
                key={choice.key}
                data-handoff-option={choice.key}
                data-chosen={selectedChoice ? "" : undefined}
                className={cn(
                  "flex cursor-pointer items-start gap-2 rounded-xs border px-2 py-1.5 text-caption",
                  selectedChoice
                    ? "border-brand/60 bg-brand/7 dark:bg-brand/12"
                    : "border-transparent hover:bg-accent/40",
                )}
              >
                <input
                  type="radio"
                  className="mt-0.5 size-3.5 accent-brand"
                  name="handoff-choice"
                  checked={selectedChoice}
                  onChange={() => pick(choice)}
                />
                <span className="min-w-0">
                  <span className="block text-foreground">{choice.label}</span>
                  {choice.detail !== "" && (
                    <span className="block text-micro text-muted-foreground">{choice.detail}</span>
                  )}
                </span>
              </label>
            );
          })}
        </fieldset>
      )}

      <Input
        className="mt-2 h-8 text-caption"
        value={text}
        maxLength={2000}
        placeholder={t(($) => $.detail.handoff.write_own)}
        aria-label={t(($) => $.detail.handoff.write_own)}
        onChange={(event) => setText(event.currentTarget.value)}
        onKeyDown={(event) => {
          if (event.key !== "Enter" || event.nativeEvent.isComposing) return;
          event.preventDefault();
          void submit();
        }}
      />

      {detail.description.trim() !== "" && (
        <div className="mt-2 rounded-md border px-3 py-2.5">
          <ReadonlyContent content={detail.description} />
        </div>
      )}

      <div className="mt-2.5 flex flex-wrap items-center gap-2">
        <label className="text-caption text-muted-foreground" htmlFor="handoff-destination">
          {t(($) => $.detail.handoff.destination)}
        </label>
        <select
          id="handoff-destination"
          value={nextStatus}
          disabled={respond.isPending || recorded !== null}
          aria-label={t(($) => $.detail.handoff.destination)}
          onChange={(event) => setDestination(event.currentTarget.value)}
          className="rounded-md border bg-background px-2 py-1 text-caption"
        >
          <option value="" disabled>
            {t(($) => $.detail.handoff.destination_placeholder)}
          </option>
          {destinations.map((status) => (
            <option key={status.key} value={status.key}>
              {status.label}
            </option>
          ))}
        </select>
        <Button
          type="button"
          size="xs"
          variant="default"
          className="ml-auto"
          disabled={!ready || respond.isPending || recorded !== null}
          onClick={() => void submit()}
        >
          {respond.isPending && (
            <Loader2 aria-hidden="true" className="animate-spin motion-reduce:animate-none" />
          )}
          {t(($) => $.detail.handoff.submit)}
        </Button>
      </div>

      {nextStatus === "" && (
        <p className="mt-1 text-caption text-muted-foreground">
          {t(($) => $.detail.handoff.destination_required)}
        </p>
      )}
      {error !== null && (
        <p role="alert" className="mt-1 text-caption text-destructive">
          {error}
        </p>
      )}
      {stale && (
        <div role="alert" className="mt-1 rounded-md border border-warning/40 px-2.5 py-1.5">
          <p className="text-caption font-medium text-foreground">
            {t(($) => $.detail.handoff.stale_title)}
          </p>
          <p className="text-caption text-muted-foreground">
            {t(($) => $.detail.handoff.stale_hint)}
          </p>
        </div>
      )}
      {recorded !== null && (
        <p className="mt-1 flex items-center gap-1.5 text-caption text-success">
          <Check aria-hidden="true" className="size-3.5" />
          {t(($) => $.detail.handoff.receipt, {
            decision: recorded.decision,
            status: labelOf(recorded.next_status),
          })}
        </p>
      )}
    </section>
  );
}
