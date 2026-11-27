// @vitest-environment jsdom

import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { TaskMemoryReport } from "@multica/core/types";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import { MemoryCompare } from "./memory-compare";

type MemoryPart = NonNullable<TaskMemoryReport["memory"]>;

const injected: MemoryPart = {
  part: "memory",
  backend: "moct",
  block: "MOCT rule: keep it clear",
  bytes: 900,
  tokens: 225,
  latency_ms: 40,
  entries: [],
};

const shadow = {
  backend: "powercontext",
  block: "PowerContext fact: the resolver lives in the daemon",
  bytes: 1200,
  tokens: 300,
  latency_ms: 150,
};

function show(
  memory: MemoryPart,
  mark: TaskMemoryReport["mark"] = null,
  api: Record<string, unknown> = {},
) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  setApiInstance({
    getWorkspaceMemoryComparison: vi.fn().mockResolvedValue({
      workspace_id: "ws-1", marked: 0,
      votes: { moct: 0, powercontext: 0, neither: 0 },
      preference_pct: { moct: 0, powercontext: 0, neither: 0 },
      backends: [],
    }),
    ...api,
  } as never);
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <MemoryCompare
        issueId="issue-1"
        taskId="task-1"
        workspaceId="ws-1"
        memory={memory}
        mark={mark}
      />
    </QueryClientProvider>,
  );
}

afterEach(() => cleanup());

describe("MemoryCompare", () => {
  it("shows nothing on a run with only one measured block", () => {
    show(injected);
    expect(screen.queryByTestId("memory-compare")).not.toBeInTheDocument();
  });

  it("shows both blocks with bytes, tokens and latency", () => {
    show({ ...injected, shadow });
    const blocks = screen.getAllByTestId("memory-compare-block");
    expect(blocks).toHaveLength(2);
    expect(screen.getByText("Injected (moct)")).toBeInTheDocument();
    expect(screen.getByText("Shadow (powercontext)")).toBeInTheDocument();
    expect(screen.getByText("MOCT rule: keep it clear")).toBeInTheDocument();
    expect(screen.getByText("PowerContext fact: the resolver lives in the daemon")).toBeInTheDocument();
    expect(screen.getByText("900 B · 225 tok · 40 ms")).toBeInTheDocument();
    expect(screen.getByText("1200 B · 300 tok · 150 ms")).toBeInTheDocument();
  });

  it("shows why a side produced no block", () => {
    show({
      ...injected,
      shadow: { ...shadow, block: undefined, bytes: 0, tokens: 0, error: "timed out after 5s" },
    });
    expect(screen.getByText("Read failed: timed out after 5s")).toBeInTheDocument();
  });

  it("saves a vote and reports who cast it", async () => {
    const setIssueTaskMemoryMark = vi.fn().mockResolvedValue({
      task_id: "task-1", choice: "powercontext", marked_by_type: "member",
      primary_bytes: 900, primary_latency_ms: 40, shadow_bytes: 1200,
      shadow_latency_ms: 150, marked_at: "2026-11-27T19:00:00Z",
    });
    show({ ...injected, shadow }, null, { setIssueTaskMemoryMark });

    fireEvent.click(screen.getByRole("button", { name: "PowerContext" }));
    await waitFor(() =>
      expect(setIssueTaskMemoryMark).toHaveBeenCalledWith("issue-1", "task-1", "powercontext"),
    );
    expect(await screen.findByTestId("memory-compare-mark")).toHaveTextContent(
      "Saved: PowerContext — by a member",
    );
    // The saved choice is the pressed one, so a reader can see the vote at rest.
    expect(screen.getByRole("button", { name: "PowerContext" })).toHaveAttribute("aria-pressed", "true");
  });

  it("renders a vote saved on an earlier visit", () => {
    show({ ...injected, shadow }, {
      task_id: "task-1", choice: "neither", marked_by_type: "agent",
      primary_bytes: 900, primary_latency_ms: 40, shadow_bytes: 1200,
      shadow_latency_ms: 150, marked_at: "2026-11-27T19:00:00Z",
    });
    expect(screen.getByTestId("memory-compare-mark")).toHaveTextContent("Saved: Neither — by an agent");
    expect(screen.getByRole("button", { name: "Neither" })).toHaveAttribute("aria-pressed", "true");
  });

  it("shows the workspace aggregate once runs are marked", async () => {
    show({ ...injected, shadow }, null, {
      getWorkspaceMemoryComparison: vi.fn().mockResolvedValue({
        workspace_id: "ws-1", marked: 4,
        votes: { moct: 2, powercontext: 1, neither: 1 },
        preference_pct: { moct: 50, powercontext: 25, neither: 25 },
        backends: [
          { backend: "moct", bytes: 900, latency_ms: 40 },
          { backend: "powercontext", bytes: 1200, latency_ms: 150 },
        ],
      }),
    });
    expect(
      await screen.findByText("4 marked · MOCT 50% · PowerContext 25% · neither 25%"),
    ).toBeInTheDocument();
    // Each row reads "<backend> median <bytes> · <latency>".
    expect(screen.getByText(/median 900 B · 40 ms/)).toBeInTheDocument();
    expect(screen.getByText(/median 1200 B · 150 ms/)).toBeInTheDocument();
  });

  it("says so when nothing in the workspace is marked yet", async () => {
    show({ ...injected, shadow });
    expect(await screen.findByText("No runs have been marked yet.")).toBeInTheDocument();
  });
});
