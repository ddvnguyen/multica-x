// @vitest-environment node

import { afterEach, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

it("reads and validates an issue-scoped task memory report", async () => {
  const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({
    task_id: "task-1",
    workspace_id: "ws-1",
    memory: { part: "memory", note: "2 entries", entries: [
      { id: "r1", type: "rule", title: "Keep it clear", body: "Use plain language." },
    ] },
  })));
  vi.stubGlobal("fetch", fetcher);

  const report = await new ApiClient("/multica").getIssueTaskMemoryReport("issue-1", "task-1");

  expect(String(fetcher.mock.calls[0]?.[0])).toBe("/multica/api/issues/issue-1/tasks/task-1/memory-report");
  expect(report.memory?.entries[0]?.body).toBe("Use plain language.");
});

// M-616: the injected and shadow blocks with their measurements, and the vote
// already cast on this run.
it("reads the shadow block and the saved mark", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
    task_id: "task-1",
    workspace_id: "ws-1",
    memory: {
      part: "memory", backend: "moct", block: "MOCT block", bytes: 900, tokens: 225,
      latency_ms: 40, entries: [],
      shadow: { backend: "powercontext", block: "PC block", bytes: 1200, tokens: 300, latency_ms: 150 },
    },
    mark: {
      task_id: "task-1", choice: "powercontext", marked_by_type: "member",
      primary_bytes: 900, primary_latency_ms: 40, shadow_bytes: 1200,
      shadow_latency_ms: 150, marked_at: "2026-11-27T19:00:00Z",
    },
  }))));

  const report = await new ApiClient("/multica").getIssueTaskMemoryReport("issue-1", "task-1");

  expect(report.memory?.bytes).toBe(900);
  expect(report.memory?.shadow?.backend).toBe("powercontext");
  expect(report.memory?.shadow?.latency_ms).toBe(150);
  expect(report.mark?.choice).toBe("powercontext");
});

// An older daemon sends no measurements and no shadow; the report must still
// parse, and the compare panel then has nothing to draw.
it("accepts a report from before the shadow existed", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
    task_id: "task-1", workspace_id: "ws-1",
    memory: { part: "memory", note: "MOCT seam is off", entries: [] },
  }))));

  const report = await new ApiClient("/multica").getIssueTaskMemoryReport("issue-1", "task-1");

  expect(report.memory?.shadow).toBeNull();
  expect(report.mark).toBeNull();
});

it("posts a vote and validates the saved mark", async () => {
  const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({
    task_id: "task-1", choice: "neither", marked_by_type: "agent",
    primary_bytes: 900, primary_latency_ms: 40, shadow_bytes: 1200,
    shadow_latency_ms: 150, marked_at: "2026-11-27T19:00:00Z",
  })));
  vi.stubGlobal("fetch", fetcher);

  const mark = await new ApiClient("/multica").setIssueTaskMemoryMark("issue-1", "task-1", "neither", "ws-1");

  expect(String(fetcher.mock.calls[0]?.[0])).toBe("/multica/api/issues/issue-1/tasks/task-1/memory-mark");
  expect(fetcher.mock.calls[0]?.[1]).toMatchObject({ method: "POST", body: JSON.stringify({ choice: "neither" }) });
  expect(mark.choice).toBe("neither");
});

// M-625: the panel POST must name the issue's workspace. The ambient route
// slug can be overwritten by another tab, and the handler then refuses the
// vote as unscoped — the lane-z evidence that prompted this fix.
it("stamps the issue's workspace on a memory vote", async () => {
  const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({
    task_id: "task-1", choice: "moct", marked_by_type: "member",
    primary_bytes: 900, primary_latency_ms: 40, shadow_bytes: 1200,
    shadow_latency_ms: 150, marked_at: "2026-11-27T19:00:00Z",
  })));
  vi.stubGlobal("fetch", fetcher);

  await new ApiClient("/multica").setIssueTaskMemoryMark("issue-1", "task-1", "moct", "ws-42");

  expect(fetcher.mock.calls[0]?.[1]).toMatchObject({
    headers: { "X-Workspace-ID": "ws-42", "X-Workspace-Slug": "" },
  });
});

it("reads the workspace comparison and falls back on a malformed one", async () => {
  const fetcher = vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({
      workspace_id: "ws-1", marked: 4,
      votes: { moct: 2, powercontext: 1, neither: 1 },
      preference_pct: { moct: 50, powercontext: 25, neither: 25 },
      backends: [{ backend: "moct", bytes: 900, latency_ms: 40 }],
    })))
    .mockResolvedValueOnce(new Response(JSON.stringify({ marked: "not a number" })));
  vi.stubGlobal("fetch", fetcher);

  const client = new ApiClient("/multica");
  const comparison = await client.getWorkspaceMemoryComparison("ws-1");
  expect(String(fetcher.mock.calls[0]?.[0])).toBe("/multica/api/workspaces/ws-1/memory-comparison");
  expect(comparison.preference_pct.moct).toBe(50);
  expect(comparison.backends[0]?.bytes).toBe(900);

  const recovered = await client.getWorkspaceMemoryComparison("ws-1");
  expect(recovered.marked).toBe(0);
  expect(recovered.votes.moct).toBe(0);
});
