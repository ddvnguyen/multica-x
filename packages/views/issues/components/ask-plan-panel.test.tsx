import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@multica/core/api";
import type { IssueAskPlan } from "@multica/core/issues/ask-plan";
import { renderWithI18n } from "../../test/i18n";
import { AskPlanPanel } from "./ask-plan-panel";

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = (await importOriginal()) as Record<string, unknown>;
  return {
    ...actual,
    api: {
      getIssueAskPlan: vi.fn(),
      answerQuestionCard: vi.fn(),
      reviewPlan: vi.fn(),
      createTaskSupplement: vi.fn(),
    },
  };
});
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("../../editor", () => ({
  ReadonlyContent: ({ content }: { content: string }) => <div data-testid="section">{content}</div>,
}));

const EMPTY: IssueAskPlan = { question_cards: [], plan: null };

const CARD_STATE: IssueAskPlan = {
  question_cards: [
    {
      id: "card-9",
      issue_id: "issue-3",
      task_id: "task-7",
      agent_id: "agent-2",
      status: "open",
      context: "Deciding the rollout order.",
      questions: [
        {
          prompt: "Which lane ships first?",
          multiple: false,
          options: [
            { label: "Web", description: "The Next.js app" },
            { label: "Mobile" },
          ],
        },
      ],
    },
  ],
  plan: null,
};

const PLAN_STATE: IssueAskPlan = {
  question_cards: [],
  plan: {
    id: "plan-4",
    issue_id: "issue-3",
    task_id: "task-7",
    title: "Split the cutover",
    status: "in_review",
    revision: 6,
    submitted_revision: 6,
    sections: [
      { sectionId: "s1", heading: "Scope", markdown: "Two lanes, z first." },
      { sectionId: "s2", heading: "Rollout", markdown: "" },
    ],
  },
};

function renderPanel() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <AskPlanPanel issueId="issue-3" />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.clearAllMocks();
});

describe("AskPlanPanel", () => {
  it("renders nothing while the read is in flight and nothing when there is no open state", async () => {
    const { promise, resolve } = Promise.withResolvers<IssueAskPlan>();
    vi.mocked(api.getIssueAskPlan).mockReturnValue(promise);

    const view = renderPanel();
    expect(view.queryByTestId("ask-plan-panel")).toBeNull();

    await act(async () => {
      resolve(EMPTY);
    });
    expect(view.queryByTestId("ask-plan-panel")).toBeNull();
    expect(view.queryByText("Which lane ships first?")).toBeNull();
  });

  it("asks the single read for this issue", async () => {
    vi.mocked(api.getIssueAskPlan).mockResolvedValue(EMPTY);
    renderPanel();
    await waitFor(() =>
      expect(api.getIssueAskPlan).toHaveBeenCalledWith("ws-1", "issue-3"),
    );
  });

  it("shows the parked card with its context and posts one answer per question", async () => {
    vi.mocked(api.getIssueAskPlan)
      .mockResolvedValueOnce(CARD_STATE)
      .mockResolvedValue(EMPTY);
    vi.mocked(api.answerQuestionCard).mockResolvedValue({
      card: CARD_STATE.question_cards[0],
      resume_task: "task-8",
    } as never);

    renderPanel();
    await screen.findByText("Deciding the rollout order.");
    expect(screen.getByText("Which lane ships first?")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("radio", { name: /Web/ }));
    fireEvent.click(screen.getByRole("button", { name: "Answer" }));

    await waitFor(() =>
      expect(api.answerQuestionCard).toHaveBeenCalledWith("ws-1", "card-9", [
        { selected: [0], text: "" },
      ]),
    );
    // The answer closes the card server-side, so the re-read drops the panel.
    await waitFor(() => expect(screen.queryByTestId("ask-plan-panel")).toBeNull());
  });

  it("reports the server's refusal instead of claiming the answer went through", async () => {
    vi.mocked(api.getIssueAskPlan).mockResolvedValue(CARD_STATE);
    vi.mocked(api.answerQuestionCard).mockRejectedValue(
      new ApiError("the question card is no longer open", 409, "Conflict"),
    );

    renderPanel();
    await screen.findByText("Which lane ships first?");
    fireEvent.click(screen.getByRole("radio", { name: /Mobile/ }));
    fireEvent.click(screen.getByRole("button", { name: "Answer" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "the question card is no longer open",
    );
    expect(screen.queryByText("Answer sent")).not.toBeInTheDocument();
  });

  it("renders the plan's sections and keeps request-changes back until a comment exists", async () => {
    vi.mocked(api.getIssueAskPlan).mockResolvedValue(PLAN_STATE);
    vi.mocked(api.reviewPlan).mockResolvedValue({
      plan: PLAN_STATE.plan,
      resume_task: "task-8",
    } as never);

    renderPanel();
    await screen.findByText("Split the cutover");
    expect(screen.getByTestId("section")).toHaveTextContent("Two lanes, z first.");
    expect(screen.getByText("Rollout")).toBeInTheDocument();
    expect(screen.getByText("No content in this section yet.")).toBeInTheDocument();

    const changes = screen.getByRole("button", { name: "Request changes" });
    expect(changes).toBeDisabled();

    fireEvent.change(screen.getByRole("textbox", { name: "What should change?" }), {
      target: { value: "Ship z first." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add comment" }));
    expect(screen.getByText("Ship z first.")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Request changes" }));
    await waitFor(() =>
      expect(api.reviewPlan).toHaveBeenCalledWith("ws-1", "plan-4", {
        revision: 6,
        verdict: "changes_requested",
        comments: ["Ship z first."],
        note: "",
      }),
    );
  });

  it("takes two presses to cancel a plan", async () => {
    vi.mocked(api.getIssueAskPlan).mockResolvedValue(PLAN_STATE);
    vi.mocked(api.reviewPlan).mockResolvedValue({
      plan: PLAN_STATE.plan,
      resume_task: "task-8",
    } as never);

    renderPanel();
    await screen.findByText("Split the cutover");

    const cancel = screen.getByRole("button", { name: "Cancel plan" });
    fireEvent.click(cancel);
    expect(api.reviewPlan).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Click again to cancel" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Click again to cancel" }));
    await waitFor(() =>
      expect(api.reviewPlan).toHaveBeenCalledWith(
        "ws-1",
        "plan-4",
        expect.objectContaining({ verdict: "cancel" }),
      ),
    );
  });

  it("leaves the other verdicts usable while cancel is merely armed", async () => {
    vi.mocked(api.getIssueAskPlan).mockResolvedValue(PLAN_STATE);

    renderPanel();
    await screen.findByText("Split the cutover");
    fireEvent.change(screen.getByRole("textbox", { name: "What should change?" }), {
      target: { value: "Ship z first." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add comment" }));

    // Arming is a step of one button, not a mode: a member who hesitates over
    // cancel must still be able to approve without reloading the issue.
    fireEvent.click(screen.getByRole("button", { name: "Cancel plan" }));
    expect(screen.getByRole("button", { name: "Approve plan" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Request changes" })).toBeEnabled();
    expect(api.reviewPlan).not.toHaveBeenCalled();
  });

  it("offers no verdict on a plan the agent still owns", async () => {
    vi.mocked(api.getIssueAskPlan).mockResolvedValue({
      question_cards: [],
      plan: { ...PLAN_STATE.plan!, status: "changes_requested" },
    });

    renderPanel();
    await screen.findByText("Split the cutover");
    expect(
      screen.getByText("Changes requested. Waiting for the agent to revise and resubmit."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve plan" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Request changes" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Cancel plan" })).toBeNull();
    expect(screen.queryByRole("textbox")).toBeNull();
  });

  it("says a draft plan has nothing to review yet rather than offering verdicts", async () => {
    vi.mocked(api.getIssueAskPlan).mockResolvedValue({
      question_cards: [],
      plan: { ...PLAN_STATE.plan!, status: "draft" },
    });

    renderPanel();
    await screen.findByText("Split the cutover");
    expect(
      screen.getByText("The agent is preparing this plan — there is nothing to review yet."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve plan" })).toBeNull();
  });
});
