// @vitest-environment jsdom

import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { DecisionsPage } from "./decisions-page";

/**
 * The Decisions page against stubbed reads and stubbed transports.
 *
 * What this file is for is the wiring the page owns: which request is selected,
 * which source body is read, which operation answers it, and what the four
 * states (loading, error, empty, answered) look like. The sources' own contracts
 * are tested where they live — this page must not re-test `/question-cards/:id`
 * or `/plans/:id/verdict`, only that it routes to them and never invents a
 * transport for a source that has none.
 */

const apiMock = vi.hoisted(() => ({
  listDecisions: vi.fn(),
  getDecisionDetail: vi.fn(),
  // The handoff form reads the issue's own timeline (the question is the
  // agent's last comment) and the workspace workflow (the destinations).
  listTimeline: vi.fn(),
  listIssueStatuses: vi.fn(),
  listIssueWorkflows: vi.fn(),
  respondDecision: vi.fn(),
}));

const navigationMock = vi.hoisted(() => ({
  pathname: "/acme/decisions",
  search: "",
  listeners: new Set<() => void>(),
  push: vi.fn((path: string) => {
    const [pathname, search = ""] = path.split("?", 2);
    navigationMock.pathname = pathname ?? "/acme/decisions";
    navigationMock.search = search;
    for (const listener of navigationMock.listeners) listener();
  }),
  replace: vi.fn((path: string) => {
    const [pathname, search = ""] = path.split("?", 2);
    navigationMock.pathname = pathname ?? "/acme/decisions";
    navigationMock.search = search;
    for (const listener of navigationMock.listeners) listener();
  }),
}));

vi.mock("@multica/core/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/api")>()),
  api: apiMock,
}));

const transports = vi.hoisted(() => ({
  answer: vi.fn(async () => undefined),
  review: vi.fn(),
  fenced: vi.fn(async () => undefined),
  supplement: vi.fn(async () => undefined),
}));

vi.mock("@multica/core/issues/mutations", () => ({
  useAnswerQuestionCard: () => ({
    mutateAsync: transports.answer,
    isPending: false,
  }),
  useReviewPlan: () => ({
    mutate: transports.review,
    isPending: false,
    variables: undefined,
  }),
  useCreateTaskSupplement: () => ({
    mutateAsync: transports.supplement,
    isPending: false,
  }),
  useAnswerNativeQuestion: () => ({
    mutateAsync: transports.fenced,
    isPending: false,
  }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws1",
}));

// The projections store the acting member's id; the name comes from the
// workspace directory this app already reads.
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getActorName: (_type: string, id: string) =>
      id === "member-1" ? "Ada Lovelace" : "Unknown",
    hasActor: (_type: string, id: string) => id === "member-1",
  }),
}));

vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspacePaths: () => ({
    issueDetail: (id: string) => `/acme/issues/${id}`,
  }),
}));

vi.mock("@multica/core/workspace/queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace/queries")>()),
  agentListOptions: (wsId: string) => ({
    queryKey: ["agents", wsId],
    queryFn: async () => [{ id: "agent-1", name: "Scout" }],
  }),
}));

vi.mock("../navigation", async () => {
  const { useSyncExternalStore } = await import("react");
  return {
    useNavigation: () => {
      useSyncExternalStore(
        (listener) => {
          navigationMock.listeners.add(listener);
          return () => navigationMock.listeners.delete(listener);
        },
        () => `${navigationMock.pathname}?${navigationMock.search}`,
      );
      return {
        get pathname() {
          return navigationMock.pathname;
        },
        get searchParams() {
          return new URLSearchParams(navigationMock.search);
        },
        hash: "",
        push: navigationMock.push,
        replace: navigationMock.replace,
      };
    },
  AppLink: ({
    href,
    children,
    ...rest
  }: {
    href: string;
    children: ReactNode;
  }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
  };
});

vi.mock("../editor", () => ({
  ReadonlyContent: ({ content }: { content: string }) => (
    <div data-readonly-content={content} />
  ),
}));

function interpolate(template: string, values?: Record<string, unknown>) {
  if (!values) return template;
  return template.replace(/\{\{(\w+)\}\}/g, (match, key: string) =>
    values[key] === undefined ? match : String(values[key]),
  );
}

vi.mock("../i18n", async () => {
  const decisions = (await import("../locales/en/decisions.json")).default;
  const issues = (await import("../locales/en/issues.json")).default;
  const bundles: Record<string, unknown> = { decisions, issues };
  return {
    useT: (namespace: string) => ({
      t: (
        select: (bundle: Record<string, unknown>) => string,
        values?: Record<string, unknown>,
      ) => interpolate(select(bundles[namespace] as Record<string, unknown>), values),
    }),
    useTimeAgo: () => () => "2 hours ago",
  };
});

const DECISIONS = [
  {
    kind: "question",
    source_id: "card-1",
    issue_id: "issue-1",
    issue_title: "Which store?",
    project_id: null,
    issue_revision: 3,
    agent_id: "agent-1",
    task_id: "task-1",
    question_seq: null,
    sort_at: "2026-11-03T09:00:00Z",
    requested_at: "2026-11-03T09:00:00Z",
    status: "open",
    response_id: null,
    actor_id: null,
  },
  {
    kind: "status",
    source_id: "issue-2",
    issue_id: "issue-2",
    issue_title: "Migrate the store",
    project_id: null,
    issue_revision: 12,
    agent_id: "agent-1",
    task_id: null,
    question_seq: null,
    sort_at: "2026-11-03T10:00:00Z",
    requested_at: "2026-11-03T10:00:00Z",
    status: "wait_for_decision",
    response_id: null,
    actor_id: null,
  },
  {
    kind: "plan",
    source_id: "plan-1",
    issue_id: "issue-1",
    issue_title: "Which store?",
    project_id: null,
    issue_revision: 3,
    agent_id: "agent-1",
    task_id: "task-1",
    question_seq: null,
    sort_at: "2026-11-03T11:00:00Z",
    requested_at: "2026-11-03T11:00:00Z",
    status: "in_review",
    response_id: null,
    actor_id: null,
  },
];

const HANDOFF_COMMENT = `I can't pick the rollout path without you.

\`\`\`decision
question: Which way should the migration go?
options:
  - key: expand
    label: Expand, then contract
    detail: Two deploys, no downtime
    next_status: in_progress
  - key: bigbang
    label: Big-bang cutover
    detail: One deploy, some downtime
    next_status: in_review
default: expand
\`\`\`

Anything else and I stay parked.`;

const WAITING_TIMELINE = [
  {
    type: "activity",
    id: "activity-1",
    actor_type: "agent",
    actor_id: "agent-1",
    created_at: "2026-11-03T09:30:00Z",
    action: "status_changed",
    details: { from: "in_progress", to: "wait_for_decision" },
  },
  {
    type: "comment",
    id: "comment-1",
    actor_type: "agent",
    actor_id: "agent-1",
    created_at: "2026-11-03T10:00:00Z",
    content: HANDOFF_COMMENT,
  },
];

const RECEIPT = {
  id: "receipt-1",
  workspace_id: "ws1",
  issue_id: "issue-2",
  client_request_id: "request-1",
  actor_id: "member-1",
  source_revision: 12,
  decision: "Expand, then contract",
  rationale: "",
  next_status: "in_progress",
  comment_id: "comment-2",
  task_id: null,
  created_at: "2026-11-03T12:00:00Z",
};

const DETAILS: Record<string, Record<string, unknown>> = {
  "question:card-1": {
    id: "card-1",
    issue_id: "issue-1",
    task_id: "task-1",
    agent_id: "agent-1",
    status: "open",
    context: "Picked up from the board",
    questions: [
      {
        prompt: "Which store?",
        multiple: false,
        options: [{ label: "Postgres" }, { label: "SQLite" }],
      },
    ],
  },
  "plan:plan-1": {
    plan: {
      id: "plan-1",
      issue_id: "issue-1",
      task_id: "task-1",
      title: "Ship the migration",
      status: "in_review",
      revision: 2,
      submitted_revision: 2,
      sections: [{ sectionId: "s1", heading: "Approach", markdown: "Do the thing." }],
    },
    review: null,
  },
  "status:issue-2": {
    id: "issue-2",
    title: "Migrate the store",
    description: "Waiting on the plan in the thread.",
    revision: 12,
    status: "wait_for_decision",
  },
};

function makeClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
}

function renderPage(client = makeClient()) {
  const view = render(
    <QueryClientProvider client={client}>
      <DecisionsPage />
    </QueryClientProvider>,
  );
  // The list and the detail can both contain the issue title or the kind name,
  // so assertions about the selected request read the detail pane only.
  const detail = (): HTMLElement => {
    const node = view.container.querySelector<HTMLElement>("[data-decision-detail]");
    if (node === null) throw new Error("no detail pane is mounted");
    return node;
  };
  return { ...view, detail };
}

beforeEach(() => {
  navigationMock.pathname = "/acme/decisions";
  navigationMock.search = "";
  navigationMock.push.mockClear();
  navigationMock.replace.mockClear();
  navigationMock.listeners.clear();
  apiMock.listDecisions.mockReset();
  apiMock.getDecisionDetail.mockReset();
  apiMock.listTimeline.mockReset().mockResolvedValue(WAITING_TIMELINE);
  apiMock.listIssueStatuses.mockReset().mockResolvedValue({ statuses: [] });
  apiMock.listIssueWorkflows.mockReset().mockResolvedValue({ workflows: [] });
  apiMock.respondDecision.mockReset().mockResolvedValue(RECEIPT);
  transports.answer.mockClear();
  transports.review.mockClear();
  transports.fenced.mockClear();
  transports.supplement.mockClear();
  apiMock.listDecisions.mockImplementation(async (_ws: string, params: { state?: string }) => ({
    items: params.state === "answered" ? ANSWERED : DECISIONS,
    pending_count: 3,
    next_cursor: null,
  }));
  apiMock.getDecisionDetail.mockImplementation(async (_ws: string, kind: string, sourceId: string) => {
    const detail = DETAILS[`${kind}:${sourceId}`];
    if (!detail) throw new Error("not found");
    return detail;
  });
});

const ANSWERED = [
  {
    ...DECISIONS[0],
    source_id: "card-9",
    status: "answered",
    response_id: "card-9",
    actor_id: "member-1",
    sort_at: "2026-11-03T12:00:00Z",
  },
];

describe("DecisionsPage", () => {
  it("stores queue selection in navigation and restores the selected state from the URL", async () => {
    const initial = renderPage();
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    const firstQuestion = initial.container.querySelector<HTMLElement>(
      '[data-decision="question:card-1"]',
    );
    if (firstQuestion === null) throw new Error("the first question is not rendered");
    fireEvent.click(firstQuestion);
    expect(navigationMock.push).toHaveBeenCalledWith(
      "/acme/decisions?decision=question%3Acard-1",
    );
    initial.unmount();

    navigationMock.search = "state=answered&decision=question%3Acard-9";
    renderPage();
    await waitFor(() => {
      expect(screen.getByRole("tab", { name: /Answered/ }).getAttribute("aria-selected")).toBe("true");
      expect(screen.getAllByRole("option")).toHaveLength(1);
    });
    expect(screen.getAllByRole("option")[0]?.getAttribute("data-decision")).toBe("question:card-9");
    expect(screen.getAllByRole("option")[0]?.getAttribute("aria-selected")).toBe("true");
  });

  it("groups the queue by issue and keeps every request as its own row", async () => {
    renderPage();
    // Two requests on "Which store?" and one handoff on "Migrate the store".
    await waitFor(() => {
      expect(screen.getAllByText("Which store?").length).toBeGreaterThan(0);
    });
    expect(screen.getByText("Migrate the store")).toBeTruthy();
    expect(screen.getAllByRole("option")).toHaveLength(3);
    const rows = screen.getAllByRole("option").map((row) => row.textContent ?? "");
    // Grouped by issue: the two requests on one issue stay together even
    // though the handoff on the other issue was asked between them.
    expect(rows[0]).toContain("Question");
    expect(rows[1]).toContain("Plan");
    expect(rows[2]).toContain("Workflow handoff");
  });

  it("selects the first request and reads its source body", async () => {
    const view = renderPage();
    await waitFor(() => expect(view.detail()).toBeTruthy());
    await waitFor(() =>
      expect(within(view.detail()).getByText("Picked up from the board")).toBeTruthy(),
    );
    // The selected row's body is the card's own question form.
    expect(within(view.detail()).getAllByText(/Which store\?/).length).toBeGreaterThan(0);
    expect(apiMock.getDecisionDetail).toHaveBeenCalledWith("ws1", "question", "card-1");
  });

  it("answers a question card through the card's own operation", async () => {
    const view = renderPage();
    await waitFor(() =>
      expect(within(view.detail()).getByText("Picked up from the board")).toBeTruthy(),
    );
    fireEvent.change(within(view.detail()).getByRole("textbox"), {
      target: { value: "Postgres, please" },
    });
    fireEvent.click(within(view.detail()).getByRole("button", { name: "Answer" }));
    await waitFor(() => expect(transports.answer).toHaveBeenCalledTimes(1));
    expect(transports.answer).toHaveBeenCalledWith({
      cardId: "card-1",
      answers: [{ selected: [], text: "Postgres, please" }],
    });
  });

  it("offers a verdict for a plan and records it on the plan endpoint", async () => {
    const view = renderPage();
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    fireEvent.click(screen.getAllByRole("option")[1]!);
    await waitFor(() =>
      expect(within(view.detail()).getByText("Ship the migration")).toBeTruthy(),
    );
    fireEvent.click(within(view.detail()).getByRole("button", { name: "Approve plan" }));
    await waitFor(() => expect(transports.review).toHaveBeenCalledTimes(1));
    const [variables] = transports.review.mock.calls[0] as unknown as [
      { planId: string; verdict: { verdict: string; revision: number } },
    ];
    expect(variables.planId).toBe("plan-1");
    expect(variables.verdict.verdict).toBe("approve");
    // The fence is the submitted revision, not the plan's working revision.
    expect(variables.verdict.revision).toBe(2);
  });

  /** The queue rows, read from the listbox so the destination select's own
   * options never count as rows. */
  const rows = (): HTMLElement[] => within(screen.getByRole("listbox")).getAllByRole("option");

  async function openHandoff(view: ReturnType<typeof renderPage>) {
    await waitFor(() => expect(rows()).toHaveLength(3));
    fireEvent.click(rows()[2]!);
    await waitFor(() =>
      expect(view.detail().querySelector("[data-handoff-form]")).not.toBeNull(),
    );
    // The question comes from the issue timeline, so the form renders its
    // fallback choices first; wait for the read that decides what is on offer.
    await waitFor(() =>
      expect(view.detail().querySelector("[data-readonly-content]")).not.toBeNull(),
    );
    return view;
  }

  describe("workflow handoff", () => {
    it("answers a structured handoff where it is asked, with its options and free text", async () => {
      const view = await openHandoff(renderPage());
      const detail = within(view.detail());

      // The question is the agent's comment, shown read-only. Its machine-readable
      // block is not part of the prose the member reads.
      await waitFor(() =>
        expect(view.detail().querySelector("[data-readonly-content]")?.getAttribute("data-readonly-content"))
          .toContain("I can't pick the rollout path without you."),
      );
      expect(
        view.detail().querySelector("[data-readonly-content]")?.getAttribute("data-readonly-content"),
      ).not.toContain("```decision");
      expect(detail.getByText("Which way should the migration go?")).toBeTruthy();

      // The block's options, with their details, as single choices.
      expect(detail.getByRole("radio", { name: /Expand, then contract/ })).toBeTruthy();
      expect(detail.getByRole("radio", { name: /Big-bang cutover/ })).toBeTruthy();
      expect(detail.getByText("Two deploys, no downtime")).toBeTruthy();
      // The recommended option is preselected, and its own destination with it.
      expect((detail.getByRole("radio", { name: /Expand, then contract/ }) as HTMLInputElement).checked).toBe(true);
      const destination = detail.getByRole("combobox", { name: "Move to" }) as HTMLSelectElement;
      expect(destination.value).toBe("in_progress");
      // The waiting step is never offered as a destination.
      expect([...destination.options].map((option) => option.value)).not.toContain("wait_for_decision");

      // A free-text box is always there, whether or not an option was picked.
      expect(detail.getByLabelText("Or write your own answer")).toBeTruthy();

      fireEvent.change(detail.getByLabelText("Or write your own answer"), {
        target: { value: "Keep the rollback for a week" },
      });
      fireEvent.click(detail.getByRole("button", { name: "Record decision" }));

      await waitFor(() => expect(apiMock.respondDecision).toHaveBeenCalledTimes(1));
      const [issueId, body] = apiMock.respondDecision.mock.calls[0] as unknown as [
        string,
        Record<string, unknown>,
      ];
      expect(issueId).toBe("issue-2");
      expect(body).toMatchObject({
        // The fence is the row's revision, the answer is the option's label, the
        // note is the rationale and the destination is the picked status.
        expected_issue_revision: 12,
        decision: "Expand, then contract",
        rationale: "Keep the rollback for a week",
        next_status: "in_progress",
      });
      expect(typeof body.client_request_id).toBe("string");
      await waitFor(() =>
        expect(detail.getByText(/Recorded Expand, then contract → In Progress/)).toBeTruthy(),
      );
    });

    it("lets a choice's own destination be overridden, and sends the override", async () => {
      const view = await openHandoff(renderPage());
      const detail = within(view.detail());
      fireEvent.click(detail.getByRole("radio", { name: /Big-bang cutover/ }));
      const destination = detail.getByRole("combobox", { name: "Move to" }) as HTMLSelectElement;
      await waitFor(() => expect(destination.value).toBe("in_review"));
      fireEvent.change(destination, { target: { value: "todo" } });
      fireEvent.click(detail.getByRole("button", { name: "Record decision" }));
      await waitFor(() => expect(apiMock.respondDecision).toHaveBeenCalledTimes(1));
      expect(
        (apiMock.respondDecision.mock.calls[0] as unknown as [string, Record<string, unknown>])[1],
      ).toMatchObject({ decision: "Big-bang cutover", next_status: "todo" });
    });

    it("answers a legacy handoff with the default choices and a written answer", async () => {
      apiMock.listTimeline.mockResolvedValue([
        WAITING_TIMELINE[0],
        {
          type: "comment",
          id: "comment-9",
          actor_type: "agent",
          actor_id: "agent-1",
          created_at: "2026-11-03T10:00:00Z",
          content: "The plan is in the thread — say the word and I'll start.",
        },
      ]);
      const view = await openHandoff(renderPage());
      const detail = within(view.detail());

      // No block: the comment is the question and the default choices are offered.
      await waitFor(() =>
        expect(detail.getByRole("radio", { name: "Approve" })).toBeTruthy(),
      );
      expect(detail.getByRole("radio", { name: "Request changes" })).toBeTruthy();
      expect(detail.getByRole("radio", { name: "Other" })).toBeTruthy();
      expect((detail.getByRole("combobox", { name: "Move to" }) as HTMLSelectElement).value).toBe(
        "in_progress",
      );

      // Nothing picked and nothing written is not a submission.
      const submit = detail.getByRole("button", { name: "Record decision" }) as HTMLButtonElement;
      expect(submit.disabled).toBe(true);

      // Free text alone is the answer: it goes as `custom`, the text as rationale.
      fireEvent.change(detail.getByLabelText("Or write your own answer"), {
        target: { value: "Not yet — wait for the freeze" },
      });
      await waitFor(() => expect(submit.disabled).toBe(false));
      fireEvent.click(submit);
      await waitFor(() => expect(apiMock.respondDecision).toHaveBeenCalledTimes(1));
      expect(
        (apiMock.respondDecision.mock.calls[0] as unknown as [string, Record<string, unknown>])[1],
      ).toMatchObject({
        decision: "custom",
        rationale: "Not yet — wait for the freeze",
        next_status: "in_progress",
      });
    });

    it("re-reads the issue and says so when the handoff moved on under the form", async () => {
      apiMock.respondDecision.mockRejectedValue(
        Object.assign(new Error("issue is not waiting for a decision"), { status: 409 }),
      );
      const view = await openHandoff(renderPage());
      const detail = within(view.detail());
      const reads = apiMock.getDecisionDetail.mock.calls.length;
      fireEvent.click(detail.getByRole("button", { name: "Record decision" }));
      await waitFor(() =>
        expect(detail.getByText("This issue changed while you were answering")).toBeTruthy(),
      );
      // A 409 means the row on screen is stale, so the page asks again.
      await waitFor(() =>
        expect(apiMock.getDecisionDetail.mock.calls.length).toBeGreaterThan(reads),
      );
    });

    it("keeps a handoff read-only once it has been answered", async () => {
      apiMock.listDecisions.mockImplementation(
        async (_ws: string, params: { state?: string }) => ({
          items:
            params.state === "answered"
              ? [
                  {
                    ...DECISIONS[1],
                    source_id: "issue-2:receipt-1",
                    status: "answered",
                    response_id: "receipt-1",
                    actor_id: "member-1",
                  },
                ]
              : DECISIONS,
          pending_count: 2,
          next_cursor: null,
        }),
      );
      apiMock.getDecisionDetail.mockResolvedValue({
        id: "issue-2",
        title: "Migrate the store",
        description: "Waiting on the plan in the thread.",
        revision: 13,
        status: "in_progress",
        response: {
          ...RECEIPT,
          decision: "Approve",
          rationale: "Reviewed the plan",
        },
      });
      const view = renderPage();
      await waitFor(() => expect(rows()).toHaveLength(3));
      fireEvent.click(screen.getByRole("tab", { name: /Answered/ }));
      await waitFor(() => expect(rows()).toHaveLength(1));
      await waitFor(() => expect(within(view.detail()).getByText("Verdict")).toBeTruthy());
      expect(view.detail().querySelector("[data-handoff-form]")).toBeNull();
      expect(within(view.detail()).queryByRole("button", { name: "Record decision" })).toBeNull();
    });
  });

  it("shows recorded answers read-only on the Answered tab", async () => {
    apiMock.getDecisionDetail.mockImplementation(async () => ({
      ...DETAILS["question:card-1"],
      status: "answered",
      answers: [{ selected: [0], text: "Postgres, please" }],
      answered_by: "member-1",
      answered_at: "2026-11-03T12:00:00Z",
    }));
    renderPage();
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    fireEvent.click(screen.getByRole("tab", { name: /Answered/ }));
    await waitFor(() =>
      expect(screen.getByText("Postgres · Postgres, please")).toBeTruthy(),
    );
    // Actor and time come with the stored answer, not just the text.
    expect(screen.getByText("Ada Lovelace")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Answer" })).toBeNull();
    expect(
      screen.getByText(
        "Recorded answers are read-only. A new request arrives as its own row.",
      ),
    ).toBeTruthy();
  });

  describe("answered question display", () => {
    // The stored answer is the list of option *indexes* the member picked, so an
    // answered ask has to resolve them against the questions it asked (M-241).
    function showAnswer(detail: Record<string, unknown>) {
      apiMock.getDecisionDetail.mockImplementation(async () => ({
        ...DETAILS["question:card-1"],
        status: "answered",
        answered_by: "member-1",
        answered_at: "2026-11-03T12:00:00Z",
        ...detail,
      }));
    }

    async function openAnswered() {
      const view = renderPage();
      await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
      fireEvent.click(screen.getByRole("tab", { name: /Answered/ }));
      await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
      await waitFor(() =>
        expect(within(view.detail()).getByText("Which store?")).toBeTruthy(),
      );
      return view;
    }

    it("renders the chosen option label, never its index", async () => {
      showAnswer({ answers: [{ selected: [1], text: "" }] });
      const view = await openAnswered();
      expect(within(view.detail()).getByText("SQLite")).toBeTruthy();
      expect(within(view.detail()).queryByText("1.2")).toBeNull();
    });

    it("comma-joins every label of a multiple answer", async () => {
      showAnswer({
        questions: [
          {
            prompt: "Which store?",
            multiple: true,
            options: [
              { label: "Postgres" },
              { label: "SQLite" },
              { label: "Mongo" },
            ],
          },
        ],
        answers: [{ selected: [0, 2], text: "" }],
      });
      const view = await openAnswered();
      expect(within(view.detail()).getByText("Postgres, Mongo")).toBeTruthy();
    });

    it("shows a written answer on its own", async () => {
      showAnswer({ answers: [{ selected: [], text: "A managed replica" }] });
      const view = await openAnswered();
      expect(within(view.detail()).getByText("A managed replica")).toBeTruthy();
      expect(within(view.detail()).queryByText("No answer")).toBeNull();
    });

    it("drops an out-of-range option index instead of rendering it", async () => {
      showAnswer({ answers: [{ selected: [7], text: "" }] });
      const view = await openAnswered();
      expect(within(view.detail()).getByText("No answer")).toBeTruthy();
      expect(within(view.detail()).queryByText("1.8")).toBeNull();
    });

    it("keeps the valid labels of an answer that also names a stale option", async () => {
      showAnswer({ answers: [{ selected: [0, 7], text: "" }] });
      const view = await openAnswered();
      expect(within(view.detail()).getByText("Postgres")).toBeTruthy();
    });
  });

  it("answers a native ask through the parked turn, and shows its receipt", async () => {
    apiMock.listDecisions.mockResolvedValue({
      items: [
        {
          ...DECISIONS[0],
          kind: "native",
          source_id: "task-1:42",
          issue_title: "Wire the bridge",
          task_id: "task-1",
          question_seq: 42,
          status: "open",
        },
      ],
      pending_count: 1,
      next_cursor: null,
    });
    apiMock.getDecisionDetail.mockResolvedValue({
      task_id: "task-1",
      message: {
        seq: 42,
        created_at: "2026-11-03T09:00:00Z",
        content: "The agent asked which runtime to use.",
        input: {
          questions: [
            {
              question: "Which runtime?",
              options: [{ label: "dsh" }, { label: "local" }],
            },
          ],
        },
      },
    });
    const view = renderPage();
    await waitFor(() =>
      expect(within(view.detail()).getByText("Which runtime?")).toBeTruthy(),
    );
    fireEvent.click(within(view.detail()).getByRole("radio", { name: /dsh/ }));
    fireEvent.click(within(view.detail()).getByRole("button", { name: "Answer" }));
    await waitFor(() => expect(transports.fenced).toHaveBeenCalledTimes(1));
    // The transcript's own route: the answer reaches the running turn fenced to
    // the sequence it was asked at, never an unfenced supplement and never a
    // second write path the queue invents for it.
    expect(transports.fenced).toHaveBeenCalledWith({
      taskId: "task-1",
      questionSeq: 42,
      content: "dsh",
    });
    expect(transports.supplement).not.toHaveBeenCalled();
    expect(transports.answer).not.toHaveBeenCalled();
    await waitFor(() => expect(within(view.detail()).getByText("Answer sent")).toBeTruthy());
  });

  it("keeps a question draft and the server's reason when the answer is refused", async () => {
    transports.answer.mockRejectedValueOnce(new Error("the question card is no longer open"));
    const view = renderPage();
    await waitFor(() =>
      expect(within(view.detail()).getByText("Picked up from the board")).toBeTruthy(),
    );
    fireEvent.change(within(view.detail()).getByRole("textbox"), {
      target: { value: "SQLite" },
    });
    fireEvent.click(within(view.detail()).getByRole("button", { name: "Answer" }));
    await waitFor(() =>
      expect(within(view.detail()).getByRole("alert").textContent).toBe(
        "the question card is no longer open",
      ),
    );
    // A refused answer is not a lost one: the member re-sends what they wrote.
    expect(
      (within(view.detail()).getByRole("textbox") as HTMLTextAreaElement).value,
    ).toBe("SQLite");
    expect(
      within(view.detail()).getByRole("button", { name: "Answer" }),
    ).not.toHaveProperty("disabled", true);
  });

  it("re-reads the queue when a plan verdict loses its fence", async () => {
    transports.review.mockImplementationOnce(
      (_variables: unknown, options?: { onError?: (cause: Error) => void }) => {
        options?.onError?.(new Error("the plan moved to another revision"));
      },
    );
    const view = renderPage();
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    fireEvent.click(screen.getAllByRole("option")[1]!);
    await waitFor(() =>
      expect(within(view.detail()).getByText("Ship the migration")).toBeTruthy(),
    );
    const reads = apiMock.getDecisionDetail.mock.calls.length;
    fireEvent.click(within(view.detail()).getByRole("button", { name: "Approve plan" }));
    await waitFor(() =>
      expect(within(view.detail()).getByText("the plan moved to another revision")).toBeTruthy(),
    );
    // A 409 says the row on screen is stale, so the page has to ask again.
    await waitFor(() =>
      expect(apiMock.getDecisionDetail.mock.calls.length).toBeGreaterThan(reads),
    );
  });

  it("shows a recorded verdict and its comments read-only", async () => {
    apiMock.listDecisions.mockImplementation(
      async (_ws: string, params: { state?: string }) => ({
        items:
          params.state === "answered"
            ? [{ ...DECISIONS[2], status: "approved", response_id: "review-1", actor_id: "member-1" }]
            : DECISIONS,
        pending_count: 2,
        next_cursor: null,
      }),
    );
    apiMock.getDecisionDetail.mockResolvedValue({
      plan: {
        id: "plan-1",
        issue_id: "issue-1",
        task_id: "task-1",
        title: "Ship the migration",
        status: "approved",
        revision: 2,
        submitted_revision: 2,
        sections: [{ sectionId: "s1", heading: "Approach", markdown: "Do the thing." }],
        approved_by: "member-1",
      },
      review: {
        id: "review-1",
        verdict: "approve",
        comments: ["Ship it"],
        reviewed_by: "member-1",
        reviewed_at: "2026-11-03T12:00:00Z",
      },
    });
    renderPage();
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    fireEvent.click(screen.getByRole("tab", { name: /Answered/ }));
    await waitFor(() => expect(screen.getByText("Ship it")).toBeTruthy());
    expect(screen.getByText(/Ada Lovelace/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Approve plan" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Request changes" })).toBeNull();
  });

  it("shows the recorded verdict of an answered workflow handoff", async () => {
    apiMock.listDecisions.mockImplementation(
      async (_ws: string, params: { state?: string }) => ({
        items:
          params.state === "answered"
            ? [
                {
                  ...DECISIONS[1],
                  // The answered projection keys a handoff by issue and receipt.
                  source_id: "issue-2:receipt-1",
                  status: "answered",
                  response_id: "receipt-1",
                  actor_id: "member-1",
                },
              ]
            : DECISIONS,
        pending_count: 2,
        next_cursor: null,
      }),
    );
    apiMock.getDecisionDetail.mockResolvedValue({
      id: "issue-2",
      title: "Migrate the store",
      description: "Waiting on the plan in the thread.",
      revision: 12,
      status: "in_progress",
      response: {
        id: "receipt-1",
        issue_id: "issue-2",
        actor_id: "member-1",
        decision: "Approve",
        rationale: "Reviewed the plan",
        next_status: "in_progress",
        created_at: "2026-11-03T12:00:00Z",
      },
    });
    const view = renderPage();
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    fireEvent.click(screen.getByRole("tab", { name: /Answered/ }));
    await waitFor(() => expect(within(view.detail()).getByText("Verdict")).toBeTruthy());
    // The verdict line carries the decision, who recorded it and when; the
    // waiting copy belongs to a handoff nobody answered, so it cannot appear.
    const verdict = within(view.detail()).getByText(/Approve/);
    expect(verdict.textContent).toContain("Ada Lovelace");
    expect(verdict.textContent).toContain("Answered 2 hours ago");
    expect(within(view.detail()).getByText("Rationale")).toBeTruthy();
    expect(
      view.detail().querySelector('[data-readonly-content="Reviewed the plan"]'),
    ).not.toBeNull();
    expect(within(view.detail()).queryByText("Waiting on a workflow decision")).toBeNull();
    expect(
      within(view.detail())
        .getAllByRole("link", { name: "Open issue" })
        .some((link) => link.getAttribute("href") === "/acme/issues/issue-2"),
    ).toBe(true);
  });

  it("keeps the waiting copy for a pending read that fails", async () => {
    apiMock.getDecisionDetail.mockRejectedValue(new Error("boom"));
    const view = renderPage();
    await waitFor(() =>
      expect(within(view.detail()).getByText("This request could not be loaded")).toBeTruthy(),
    );
    expect(
      within(view.detail()).getByText("It is still waiting — nothing was answered."),
    ).toBeTruthy();
  });

  it("does not tell the reader nothing was answered when an answered read fails", async () => {
    apiMock.getDecisionDetail.mockRejectedValue(new Error("boom"));
    const view = renderPage();
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    fireEvent.click(screen.getByRole("tab", { name: /Answered/ }));
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
    await waitFor(() =>
      expect(
        within(view.detail()).getByText("Could not read this request. Try again."),
      ).toBeTruthy(),
    );
    expect(
      within(view.detail()).queryByText("It is still waiting — nothing was answered."),
    ).toBeNull();
  });

  it("renders a failed read as an error with a retry, never as an empty queue", async () => {
    apiMock.listDecisions.mockRejectedValue(new Error("boom"));
    renderPage();
    await waitFor(() =>
      expect(screen.getByText("Decisions could not be loaded")).toBeTruthy(),
    );
    expect(screen.queryByText("Nothing waiting")).toBeNull();
    expect(screen.getAllByRole("button", { name: /Retry/ }).length).toBeGreaterThan(0);
  });
});
