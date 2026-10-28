// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  EMPTY_ISSUE_ASK_PLAN,
  cardQuestionsToAgentQuestions,
  draftsToCardAnswers,
  hasAskPlanState,
  isPlanVerdictable,
  planVerdictBody,
  type AgentPlan,
  type AskPlanQuestionCard,
} from "./ask-plan";

function card(overrides: Partial<AskPlanQuestionCard> = {}): AskPlanQuestionCard {
  return {
    id: "card-1",
    issue_id: "issue-1",
    task_id: "task-1",
    agent_id: "agent-1",
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
    ...overrides,
  };
}

function plan(overrides: Partial<AgentPlan> = {}): AgentPlan {
  return {
    id: "plan-1",
    issue_id: "issue-1",
    task_id: "task-1",
    title: "Split the cutover",
    status: "in_review",
    revision: 3,
    submitted_revision: 3,
    sections: [{ sectionId: "s1", heading: "Scope", markdown: "Two lanes." }],
    ...overrides,
  };
}

describe("cardQuestionsToAgentQuestions", () => {
  it("maps the card's wire fields onto the shared question form", () => {
    const [mapped] = cardQuestionsToAgentQuestions(card());
    expect(mapped).toEqual({
      id: "card-1:1",
      question: "Which lane ships first?",
      options: [{ label: "Web", description: "The Next.js app" }, { label: "Mobile" }],
      multiSelect: false,
    });
  });

  it("numbers questions by their position, one distinct id each", () => {
    const mapped = cardQuestionsToAgentQuestions(
      card({
        questions: [
          { prompt: "First?", multiple: false, options: [{ label: "A" }, { label: "B" }] },
          { prompt: "Second?", multiple: true, options: [{ label: "A" }, { label: "B" }] },
        ],
      }),
    );
    expect(mapped.map((question) => question.id)).toEqual(["card-1:1", "card-1:2"]);
    expect(mapped[0]?.multiSelect).toBe(false);
    expect(mapped[1]?.multiSelect).toBe(true);
  });

  it("treats a blank option description as absent so the form omits the line", () => {
    const [mapped] = cardQuestionsToAgentQuestions(
      card({
        questions: [
          {
            prompt: "Pick one",
            multiple: false,
            options: [{ label: "Web", description: "" }, { label: "Mobile" }],
          },
        ],
      }),
    );
    expect(mapped?.options).toEqual([{ label: "Web" }, { label: "Mobile" }]);
  });
});

describe("draftsToCardAnswers", () => {
  it("emits one answer per question, in question order", () => {
    const answers = draftsToCardAnswers([
      { selected: [1], text: "" },
      { selected: [], text: " Whatever works" },
    ]);
    expect(answers).toEqual([
      { selected: [1], text: "" },
      { selected: [], text: "Whatever works" },
    ]);
  });

  it("sorts the selection so the stored answer does not depend on click order", () => {
    expect(draftsToCardAnswers([{ selected: [2, 0], text: "" }])[0]?.selected).toEqual([0, 2]);
  });

  it("reads whitespace-only text as no answer at all", () => {
    expect(draftsToCardAnswers([{ selected: [], text: "   \n " }])[0]?.text).toBe("");
  });
});

describe("isPlanVerdictable", () => {
  it("accepts an in_review plan that carries the revision under review", () => {
    expect(isPlanVerdictable(plan())).toBe(true);
  });

  it.each([["draft"], ["changes_requested"], ["approved"], ["cancelled"]])(
    "refuses a %s plan",
    (status) => {
      expect(isPlanVerdictable(plan({ status }))).toBe(false);
    },
  );

  it("refuses an in_review plan with no submitted revision to fence on", () => {
    expect(isPlanVerdictable(plan({ submitted_revision: undefined }))).toBe(false);
  });

  it("refuses nothing at all when no plan is open", () => {
    expect(isPlanVerdictable(null)).toBe(false);
    expect(isPlanVerdictable(undefined)).toBe(false);
  });
});

describe("planVerdictBody", () => {
  it("fences the verdict on the revision under review, not the latest write", () => {
    expect(planVerdictBody(plan({ revision: 7, submitted_revision: 5 }), "approve", [])).toEqual({
      revision: 5,
      verdict: "approve",
      comments: [],
      note: "",
    });
  });

  it("carries the member's comments and leaves the note empty", () => {
    const body = planVerdictBody(plan(), "changes_requested", ["Split it.", "Ship z first."]);
    expect(body.comments).toEqual(["Split it.", "Ship z first."]);
    expect(body.note).toBe("");
  });

  it("copies the comment list so later edits cannot mutate a sent payload", () => {
    const comments = ["Keep this"];
    const body = planVerdictBody(plan(), "approve", comments);
    comments.push("and not this");
    expect(body.comments).toEqual(["Keep this"]);
  });
});

describe("hasAskPlanState", () => {
  it("reads an unanswered query as nothing to render", () => {
    expect(hasAskPlanState(undefined)).toBe(false);
    expect(hasAskPlanState(null)).toBe(false);
  });

  it("reads the empty response as nothing to render", () => {
    expect(hasAskPlanState(EMPTY_ISSUE_ASK_PLAN)).toBe(false);
    expect(hasAskPlanState({ question_cards: [], plan: null })).toBe(false);
  });

  it("renders when either an open card or an open plan is present", () => {
    expect(hasAskPlanState({ question_cards: [card()], plan: null })).toBe(true);
    expect(hasAskPlanState({ question_cards: [], plan: plan() })).toBe(true);
  });
});
