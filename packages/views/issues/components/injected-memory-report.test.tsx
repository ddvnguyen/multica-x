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

  // M-132 E3: the on-demand memory tools, as the stored transcript proves them.
  it("says unavailable when the transcript could not be read", async () => {
    show(vi.fn().mockResolvedValue({
      task_id: "task-1", workspace_id: "ws-1", memory: { part: "memory", entries: [] },
      memory_tools: { available: false, calls: [] },
    }));
    expect(
      await screen.findByText(/transcript is unavailable/i),
    ).toBeInTheDocument();
    expect(screen.queryByText("No memory tool calls in this run.")).not.toBeInTheDocument();
  });

  it("says unavailable when the server predates the tool list", async () => {
    show(vi.fn().mockResolvedValue({
      task_id: "task-1", workspace_id: "ws-1", memory: { part: "memory", entries: [] },
    }));
    expect(
      await screen.findByText(/transcript is unavailable/i),
    ).toBeInTheDocument();
  });

  it("says no memory tool calls when the run made none", async () => {
    show(vi.fn().mockResolvedValue({
      task_id: "task-1", workspace_id: "ws-1", memory: { part: "memory", entries: [] },
      memory_tools: { available: true, calls: [] },
    }));
    expect(
      await screen.findByText("No memory tool calls in this run."),
    ).toBeInTheDocument();
  });

  it("lists each memory tool call with its arguments, result and error state", async () => {
    show(vi.fn().mockResolvedValue({
      task_id: "task-1", workspace_id: "ws-1", memory: { part: "memory", entries: [] },
      memory_tools: {
        available: true,
        calls: [
          {
            seq: 2, call_id: "a", tool: "mcp__moct__memory_search", name: "memory_search",
            arguments: { q: "tier resolver" }, has_result: true, result: '{"records":[{"id":"r1"}]}',
          },
          {
            seq: 6, call_id: "b", tool: "memory_get", name: "memory_get",
            arguments: { ids: ["r1"] }, has_result: false, error: "moct is unreachable",
          },
          {
            seq: 9, call_id: "c", tool: "memory_note", name: "memory_note",
            arguments: {}, has_result: false, result_truncated: true,
          },
        ],
      },
    }));
    expect(await screen.findAllByTestId("memory-tool-call")).toHaveLength(3);
    expect(screen.getByText("memory_search")).toBeInTheDocument();
    expect(screen.getByText(/tier resolver/)).toBeInTheDocument();
    expect(screen.getByText('{"records":[{"id":"r1"}]}')).toBeInTheDocument();
    expect(screen.getByText(/Run error: moct is unreachable/)).toBeInTheDocument();
    expect(screen.getByText("No result was recorded for this call.")).toBeInTheDocument();
    expect(screen.getByText("Result truncated.")).toBeInTheDocument();
  });
});
