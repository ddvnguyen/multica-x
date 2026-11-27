"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { issueKeys } from "@multica/core/issues";
import { workspaceKeys, workspaceMemoryComparisonOptions } from "@multica/core/workspace";
import type {
  MemoryBlockRecord,
  MemoryCompareChoice,
  MemoryCompareMark,
  TaskMemoryReport,
} from "@multica/core/types";
import { toast } from "sonner";
import { useT } from "../../i18n";

type MemoryPart = NonNullable<TaskMemoryReport["memory"]>;

/**
 * The injected side as a block record. The memory part carries its own
 * measurements (M-616); a part from a daemon that predates them has no block,
 * and the panel says so rather than drawing an empty box.
 */
function injectedRecord(memory: MemoryPart): MemoryBlockRecord {
  return {
    backend: memory.backend ?? "moct",
    block: memory.block,
    bytes: memory.bytes ?? 0,
    tokens: memory.tokens ?? 0,
    latency_ms: memory.latency_ms ?? 0,
  };
}

function CompareBlock({
  title,
  record,
}: {
  title: string;
  record: MemoryBlockRecord;
}) {
  const { t } = useT("issues");
  return (
    <div data-testid="memory-compare-block" className="min-w-0 rounded-md bg-muted/40 px-3 py-2">
      <p className="font-medium text-foreground">{title}</p>
      <p className="mt-0.5 text-caption text-muted-foreground">
        {t(($) => $.execution_log.compare_metrics, {
          bytes: record.bytes,
          tokens: record.tokens,
          latency: record.latency_ms,
        })}
      </p>
      {record.error ? (
        <p className="mt-1 break-words text-destructive">
          {t(($) => $.execution_log.compare_error, { error: record.error })}
        </p>
      ) : record.block ? (
        <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap break-words text-caption">
          {record.block}
        </pre>
      ) : (
        <p className="mt-1 text-muted-foreground">{t(($) => $.execution_log.compare_no_block)}</p>
      )}
      {record.note && <p className="mt-1 break-words text-caption">{record.note}</p>}
    </div>
  );
}

/**
 * The workspace aggregate: how the judged runs split, and what each backend
 * cost on them. Counts come from the server so two clients never disagree
 * about a percentage; the panel only formats them.
 */
function CompareAggregate({ workspaceId }: { workspaceId: string }) {
  const { t } = useT("issues");
  const comparison = useQuery(workspaceMemoryComparisonOptions(workspaceId));
  const data = comparison.data;
  if (!workspaceId) return null;
  return (
    <div data-testid="memory-compare-aggregate" className="mt-3 border-t border-border/60 pt-2">
      <h5 className="font-medium text-foreground">
        {t(($) => $.execution_log.compare_aggregate)}
      </h5>
      {comparison.isPending ? (
        <p role="status" className="mt-1 text-muted-foreground">
          {t(($) => $.execution_log.compare_aggregate_loading)}
        </p>
      ) : !data || data.marked === 0 ? (
        <p className="mt-1 text-muted-foreground">
          {t(($) => $.execution_log.compare_aggregate_empty)}
        </p>
      ) : (
        <div className="mt-1 space-y-1">
          <p>
            {t(($) => $.execution_log.compare_aggregate_votes, {
              marked: data.marked,
              moct: data.preference_pct.moct,
              powercontext: data.preference_pct.powercontext,
              neither: data.preference_pct.neither,
            })}
          </p>
          <ul className="space-y-0.5">
            {data.backends.map((backend) => (
              <li key={backend.backend}>
                <span className="text-foreground">{backend.backend}</span>{" "}
                {t(($) => $.execution_log.compare_median, {
                  bytes: backend.bytes,
                  latency: backend.latency_ms,
                })}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

const CHOICES: MemoryCompareChoice[] = ["moct", "powercontext", "neither"];

/**
 * M-616 Memory compare: the run's injected memory block beside the shadow block
 * read from the second backend on the same claim, one click to say which was
 * more useful, and the workspace tally that click feeds.
 *
 * Rendered only when the run recorded a shadow block. A run with one block has
 * no comparison to show and no vote to cast, and the server refuses one too —
 * a guess counted as evidence is worse than no number at all.
 */
export function MemoryCompare({
  issueId,
  taskId,
  workspaceId,
  memory,
  mark,
}: {
  issueId: string;
  taskId: string;
  workspaceId: string;
  memory: MemoryPart;
  mark: MemoryCompareMark | null;
}) {
  const { t } = useT("issues");
  const qc = useQueryClient();
  const shadow = memory.shadow;
  const injected = injectedRecord(memory);

  const vote = useMutation({
    mutationFn: (choice: MemoryCompareChoice) =>
      api.setIssueTaskMemoryMark(issueId, taskId, choice),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: issueKeys.taskMemoryReport(issueId, taskId) });
      void qc.invalidateQueries({ queryKey: workspaceKeys.memoryComparison(workspaceId) });
    },
    onError: (error) => {
      toast.error(error instanceof Error && error.message ? error.message : t(($) => $.execution_log.compare_vote_failed));
    },
  });

  if (!shadow) return null;
  // MOCT is a product name and keeps its spelling in every locale; the other
  // two are words the reader has to understand.
  const labels: Record<MemoryCompareChoice, string> = {
    moct: "MOCT",
    powercontext: t(($) => $.execution_log.compare_choice_powercontext),
    neither: t(($) => $.execution_log.compare_choice_neither),
  };
  const labelOf = (choice: string) =>
    labels[choice as MemoryCompareChoice] ?? choice;
  const chosen = vote.isPending ? undefined : (vote.data?.choice ?? mark?.choice);
  const markedBy = vote.data?.marked_by_type ?? mark?.marked_by_type;

  return (
    <section
      data-testid="memory-compare"
      aria-label={t(($) => $.execution_log.memory_compare)}
      className="mt-4 min-w-0 border-t border-border/60 pt-3"
    >
      <h4 className="mb-2 font-medium">{t(($) => $.execution_log.memory_compare)}</h4>
      <div className="grid gap-2 sm:grid-cols-2">
        <CompareBlock
          title={t(($) => $.execution_log.compare_injected, { backend: injected.backend })}
          record={injected}
        />
        <CompareBlock
          title={t(($) => $.execution_log.compare_shadow, { backend: shadow.backend })}
          record={shadow}
        />
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <span className="text-muted-foreground">{t(($) => $.execution_log.compare_mark_title)}</span>
        {CHOICES.map((choice) => {
          const active = chosen === choice;
          return (
            <button
              key={choice}
              type="button"
              aria-pressed={active}
              disabled={vote.isPending}
              onClick={() => vote.mutate(choice)}
              className={
                active
                  ? "rounded-md border border-foreground/30 bg-foreground/10 px-2 py-0.5 font-medium text-foreground"
                  : "rounded-md border border-border px-2 py-0.5 hover:bg-muted/60"
              }
            >
              {labels[choice]}
            </button>
          );
        })}
      </div>
      {chosen && markedBy && (
        <p data-testid="memory-compare-mark" className="mt-1 text-caption text-muted-foreground">
          {t(($) => $.execution_log.compare_marked_by, {
            choice: labelOf(chosen),
            who:
              markedBy === "agent"
                ? t(($) => $.execution_log.compare_by_agent)
                : t(($) => $.execution_log.compare_by_member),
          })}
        </p>
      )}
      <CompareAggregate workspaceId={workspaceId} />
    </section>
  );
}
