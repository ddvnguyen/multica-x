"use client";

import { useQuery } from "@tanstack/react-query";
import { taskMemoryReportOptions } from "@multica/core/issues";
import type { MemoryToolCall, TaskMemoryReport } from "@multica/core/types";
import { useT } from "../../i18n";

/** Longest argument JSON shown inline before it is cut with an ellipsis. */
const ARGUMENT_PREVIEW = 160;

function argumentPreview(call: MemoryToolCall): string {
  if (call.arguments === undefined || call.arguments === null) return "";
  let text: string;
  try {
    text = JSON.stringify(call.arguments);
  } catch {
    return "";
  }
  if (!text || text === "{}") return "";
  return text.length > ARGUMENT_PREVIEW ? `${text.slice(0, ARGUMENT_PREVIEW)}…` : text;
}

/**
 * M-132 E3: the on-demand MOCT memory tools the run called, from its own stored
 * transcript. Only what the rows prove is shown — name, arguments, result,
 * order, and a run error recorded while the call was the latest. It does not
 * claim which records reached the model beyond these calls and the initial
 * inject above.
 */
function MemoryToolsUsed({ report }: { report: TaskMemoryReport | undefined }) {
  const { t } = useT("issues");
  const tools = report?.memory_tools;
  return (
    <div data-testid="memory-tools-used" className="space-y-2">
      <h4 className="font-medium text-foreground">
        {t(($) => $.execution_log.memory_tools_title)}
      </h4>
      {!tools?.available ? (
        <p>{t(($) => $.execution_log.memory_tools_unavailable)}</p>
      ) : tools.calls.length === 0 ? (
        <p>{t(($) => $.execution_log.memory_tools_empty)}</p>
      ) : (
        <ol className="space-y-2">
          {tools.calls.map((call) => {
            const args = argumentPreview(call);
            return (
              <li
                key={`${call.seq}-${call.call_id ?? call.name}`}
                data-testid="memory-tool-call"
                className="min-w-0 rounded-md bg-muted/40 px-3 py-2"
              >
                <p className="flex min-w-0 flex-wrap items-baseline gap-x-2">
                  <span className="font-medium text-foreground">{call.name}</span>
                  {args && (
                    <code className="min-w-0 break-all text-caption text-muted-foreground">
                      {args}
                    </code>
                  )}
                </p>
                {call.error ? (
                  <p className="mt-1 break-words text-destructive">
                    {t(($) => $.execution_log.memory_tools_error, { error: call.error })}
                  </p>
                ) : !call.has_result ? (
                  <p className="mt-1">{t(($) => $.execution_log.memory_tools_no_result)}</p>
                ) : (
                  <pre className="mt-1 max-h-32 overflow-auto whitespace-pre-wrap break-words text-caption">
                    {call.result}
                  </pre>
                )}
                {call.result_truncated && (
                  <p className="mt-1 text-caption">
                    {t(($) => $.execution_log.memory_tools_truncated)}
                  </p>
                )}
              </li>
            );
          })}
        </ol>
      )}
    </div>
  );
}

export function InjectedMemoryReport({ issueId, taskId }: { issueId: string; taskId: string }) {
  const { t } = useT("issues");
  const report = useQuery(taskMemoryReportOptions(issueId, taskId));
  const memory = report.data?.memory;
  const entries = memory?.entries ?? [];

  return (
    <section aria-label={t(($) => $.execution_log.memory_report)} className="mt-4 min-w-0 border-t border-border/60 pt-3">
      <h3 className="mb-2 font-medium">{t(($) => $.execution_log.memory_report)}</h3>
      {report.isPending ? (
        <p role="status" className="text-muted-foreground">{t(($) => $.execution_log.memory_loading)}</p>
      ) : report.isError ? (
        <div role="alert" className="text-muted-foreground">
          <p>{t(($) => $.execution_log.memory_load_failed)}</p>
          <button type="button" className="mt-1 underline underline-offset-2" onClick={() => void report.refetch()}>
            {t(($) => $.execution_log.memory_retry)}
          </button>
        </div>
      ) : (
        <div className="space-y-3 text-muted-foreground">
          <div className="space-y-2">
            {memory?.note && <p className="break-words">{memory.note}</p>}
            {entries.length === 0 ? (
              !memory?.note && <p>{t(($) => $.execution_log.memory_empty)}</p>
            ) : (
              <ul className="space-y-2">
                {entries.map((entry) => (
                  <li key={entry.id} className="min-w-0 rounded-md bg-muted/40 px-3 py-2">
                    <p className="font-medium text-foreground">[{entry.type}] {entry.title || entry.id}</p>
                    {entry.body && <p className="mt-1 whitespace-pre-wrap break-words">{entry.body}</p>}
                  </li>
                ))}
              </ul>
            )}
          </div>
          <MemoryToolsUsed report={report.data} />
        </div>
      )}
    </section>
  );
}
