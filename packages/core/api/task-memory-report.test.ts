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
