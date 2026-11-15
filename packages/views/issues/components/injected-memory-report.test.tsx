// @vitest-environment jsdom

import { cleanup, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import { InjectedMemoryReport } from "./injected-memory-report";

function show(getIssueTaskMemoryReport: (issueId: string, taskId: string) => Promise<unknown>) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  setApiInstance({ getIssueTaskMemoryReport } as never);
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <InjectedMemoryReport issueId="issue-1" taskId="task-1" />
    </QueryClientProvider>,
  );
}

afterEach(() => cleanup());

describe("InjectedMemoryReport", () => {
  it("shows an empty report", async () => {
    show(vi.fn().mockResolvedValue({
      task_id: "task-1", workspace_id: "ws-1", memory: { part: "memory", entries: [] },
    }));
    expect(await screen.findByText("No memory was injected.")).toBeInTheDocument();
  });

  it("shows injected entries", async () => {
    show(vi.fn().mockResolvedValue({
      task_id: "task-1", workspace_id: "ws-1", memory: {
        part: "memory", entries: [{ id: "r1", type: "rule", title: "Keep it clear", body: "Use plain language." }],
      },
    }));
    expect(await screen.findByText("[rule] Keep it clear")).toBeInTheDocument();
    expect(screen.getByText("Use plain language.")).toBeInTheDocument();
  });

  it.each(["workspace switched the pre-run memory block off", "moct pre-run inject failed; the run went without memory"])(
    "shows the no-injection reason: %s",
    async (note) => {
      show(vi.fn().mockResolvedValue({
        task_id: "task-1", workspace_id: "ws-1", memory: { part: "memory", note, entries: [] },
      }));
      expect(await screen.findByText(note)).toBeInTheDocument();
    },
  );
});
