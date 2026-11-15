"use client";

import { useQuery } from "@tanstack/react-query";
import { taskMemoryReportOptions } from "@multica/core/issues";
import { useT } from "../../i18n";

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
        <div className="space-y-2 text-muted-foreground">
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
      )}
    </section>
  );
}
