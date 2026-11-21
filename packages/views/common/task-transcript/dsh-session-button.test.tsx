// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import type { AgentTask } from "@multica/core/types/agent";
import { describe, expect, it } from "vitest";
import { DshSessionButton } from "./dsh-session-button";

const baseTask: AgentTask = {
  id: "4a2e8d1c-7f9b-4e2a-9c1d-123456789abc",
  agent_id: "agent-1",
  runtime_id: "runtime-dsh",
  issue_id: "issue-1",
  status: "completed",
  priority: 0,
  dispatched_at: "2026-05-15T10:00:05.000Z",
  started_at: "2026-05-15T10:00:06.000Z",
  completed_at: "2026-05-15T10:01:00.000Z",
  result: null,
  error: null,
  created_at: "2026-05-15T10:00:00.000Z",
};

describe("DshSessionButton", () => {
  it("renders nothing when the server sent no session URL", () => {
    const { container } = render(
      <DshSessionButton task={baseTask} title="Open session" />,
    );
    expect(container).toBeEmptyDOMElement();
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("opens the run's own session in a new tab", () => {
    render(
      <DshSessionButton
        task={{
          ...baseTask,
          dsh_session_url: "http://127.0.0.1:4320/?dsh_session=ses-abc",
        }}
        title="Open session"
      />,
    );

    const link = screen.getByRole("link", { name: "Open session" });
    expect(link).toHaveAttribute("href", "http://127.0.0.1:4320/?dsh_session=ses-abc");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("keeps two runs' links distinct", () => {
    render(
      <>
        <DshSessionButton
          task={{ ...baseTask, dsh_session_url: "http://dsh/?dsh_session=ses-first" }}
          title="Open session"
        />
        <DshSessionButton
          task={{ ...baseTask, dsh_session_url: "http://dsh/?dsh_session=ses-second" }}
          title="Open session"
        />
      </>,
    );

    const hrefs = screen.getAllByRole("link").map((link) => link.getAttribute("href"));
    expect(hrefs).toEqual([
      "http://dsh/?dsh_session=ses-first",
      "http://dsh/?dsh_session=ses-second",
    ]);
  });
});
