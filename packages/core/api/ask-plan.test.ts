// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

function respond(body: unknown, status = 200) {
  const fetch = vi
    .fn()
    .mockResolvedValue(new Response(JSON.stringify(body), { status }));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

const CARD = {
  id: "card-9",
  issue_id: "issue-3",
  task_id: "task-7",
  agent_id: "agent-2",
  status: "open",
  context: "Pick the lane.",
  questions: [
    {
      prompt: "Which lane?",
      multiple: false,
      options: [{ label: "Web", description: "Next.js" }, { label: "Mobile" }],
    },
  ],
};

const PLAN = {
  id: "plan-4",
  issue_id: "issue-3",
  task_id: "task-7",
  title: "Split the cutover",
  status: "in_review",
  revision: 6,
  submitted_revision: 6,
  sections: [{ sectionId: "s1", heading: "Scope", markdown: "Two lanes." }],
};

describe("getIssueAskPlan", () => {
  it("reads the single island endpoint for the named issue", async () => {
    const fetch = respond({ question_cards: [CARD], plan: PLAN });
    const api = new ApiClient("https://api.example.test");

    const state = await api.getIssueAskPlan("ws-1", "issue-3");

    const url = new URL(fetch.mock.calls[0]![0]);
    expect(url.pathname).toBe("/api/workspaces/ws-1/issues/issue-3/ask-plan");
    expect(fetch.mock.calls[0]![1]?.method ?? "GET").toBe("GET");
    expect(state.question_cards).toHaveLength(1);
    expect(state.question_cards[0]?.questions[0]?.options[1]?.label).toBe("Mobile");
    expect(state.plan?.submitted_revision).toBe(6);
    expect(state.plan?.sections[0]?.heading).toBe("Scope");
  });

  it("answers the empty state rather than throwing when the body drifts", async () => {
    respond({ question_cards: "not-a-list" });
    const api = new ApiClient("https://api.example.test");
    await expect(api.getIssueAskPlan("ws-1", "issue-3")).resolves.toEqual({
      question_cards: [],
    });
  });

  it("keeps a card whose legacy options field came back null", async () => {
    respond({
      question_cards: [{ ...CARD, questions: [{ prompt: "Which?", multiple: false, options: null }] }],
      plan: null,
    });
    const api = new ApiClient("https://api.example.test");
    const state = await api.getIssueAskPlan("ws-1", "issue-3");
    expect(state.question_cards[0]?.questions[0]?.options).toEqual([]);
    expect(state.plan ?? null).toBeNull();
  });

  it("tolerates fields it does not know about", async () => {
    respond({ question_cards: [{ ...CARD, future_field: true }], plan: { ...PLAN, future: 1 } });
    const api = new ApiClient("https://api.example.test");
    const state = await api.getIssueAskPlan("ws-1", "issue-3");
    expect(state.question_cards[0]?.id).toBe("card-9");
    expect(state.plan?.title).toBe("Split the cutover");
  });
});

describe("answerQuestionCard", () => {
  it("posts one answer per question to the card's answer route", async () => {
    const fetch = respond({ card: { ...CARD, status: "answered" }, resume_task: "task-8" });
    const api = new ApiClient("https://api.example.test");

    await api.answerQuestionCard("ws-1", "card-9", [
      { selected: [1], text: "" },
      { selected: [], text: "Mobile first" },
    ]);

    const url = new URL(fetch.mock.calls[0]![0]);
    expect(url.pathname).toBe("/api/workspaces/ws-1/question-cards/card-9/answer");
    expect(fetch.mock.calls[0]![1]?.method).toBe("POST");
    expect(JSON.parse(fetch.mock.calls[0]![1]?.body)).toEqual({
      answers: [{ selected: [1], text: "" }, { selected: [], text: "Mobile first" }],
    });
  });
});

describe("reviewPlan", () => {
  it("posts the verdict fenced on the revision under review", async () => {
    const fetch = respond({ plan: { ...PLAN, status: "approved" }, resume_task: "task-8" });
    const api = new ApiClient("https://api.example.test");

    await api.reviewPlan("ws-1", "plan-4", {
      revision: 6,
      verdict: "changes_requested",
      comments: ["Ship z first."],
      note: "",
    });

    const url = new URL(fetch.mock.calls[0]![0]);
    expect(url.pathname).toBe("/api/workspaces/ws-1/plans/plan-4/verdict");
    expect(fetch.mock.calls[0]![1]?.method).toBe("POST");
    expect(JSON.parse(fetch.mock.calls[0]![1]?.body)).toEqual({
      revision: 6,
      verdict: "changes_requested",
      comments: ["Ship z first."],
      note: "",
    });
  });

  it("surfaces a conflict as an ApiError carrying the server's structured body", async () => {
    respond(
      { error: "stale_plan_revision", current_revision: 8, current_status: "in_review" },
      409,
    );
    const api = new ApiClient("https://api.example.test");

    await expect(
      api.reviewPlan("ws-1", "plan-4", { revision: 6, verdict: "approve", comments: [], note: "" }),
    ).rejects.toMatchObject({
      name: "ApiError",
      status: 409,
      message: "stale_plan_revision",
      body: { error: "stale_plan_revision", current_revision: 8 },
    });
  });
});
