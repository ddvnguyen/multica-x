// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { TimelineEntry } from "../types";
import {
  handoffDecision,
  parseDecisionBlock,
  readHandoffContext,
  stripDecisionBlock,
} from "./handoff";

const COMMENT = `I need a direction before I touch the schema.

\`\`\`decision
question: Which way should the migration go?
options:
  - key: expand
    label: Expand, then contract
    detail: Two deploys, no downtime
    next_status: in_progress
  - key: bigbang
    label: Big-bang cutover
default: expand
\`\`\`

Anything else and I stay parked.`;

describe("parseDecisionBlock", () => {
  it("reads the question, its options and the recommended one", () => {
    expect(parseDecisionBlock(COMMENT)).toEqual({
      question: "Which way should the migration go?",
      options: [
        {
          key: "expand",
          label: "Expand, then contract",
          detail: "Two deploys, no downtime",
          nextStatus: "in_progress",
        },
        { key: "bigbang", label: "Big-bang cutover", detail: "", nextStatus: null },
      ],
      defaultKey: "expand",
    });
  });

  it("returns null for a comment without a block, so the form can fall back", () => {
    expect(parseDecisionBlock("Please approve the plan above.")).toBeNull();
    // A fence with no options is not a choice list either.
    expect(parseDecisionBlock("```decision\nquestion: Go?\n```")).toBeNull();
  });

  it("keys options that carry none, and resolves a label as the default", () => {
    const block = parseDecisionBlock(
      "```decision\noptions:\n  - Approve\n  - Reject\ndefault: Reject\n```",
    );
    expect(block?.options.map((option) => [option.key, option.label])).toEqual([
      ["1", "Approve"],
      ["2", "Reject"],
    ]);
    expect(block?.defaultKey).toBe("2");
  });

  it("ignores unknown fields and strips quotes", () => {
    const block = parseDecisionBlock(
      '```decision\nquestion: "Ship it?"\npriority: high\noptions:\n  - key: a\n    label: "Yes"\n```',
    );
    expect(block?.question).toBe("Ship it?");
    expect(block?.options).toEqual([{ key: "a", label: "Yes", detail: "", nextStatus: null }]);
  });

  it("leaves the prose without its machine-readable block", () => {
    expect(stripDecisionBlock(COMMENT)).toBe(
      "I need a direction before I touch the schema.\n\n\n\nAnything else and I stay parked.",
    );
    expect(stripDecisionBlock("No block here.")).toBe("No block here.");
  });
});

function entry(overrides: Partial<TimelineEntry>): TimelineEntry {
  return {
    type: "activity",
    id: "entry",
    actor_type: "agent",
    actor_id: "agent-1",
    created_at: "2026-11-03T09:00:00Z",
    ...overrides,
  };
}

describe("readHandoffContext", () => {
  it("takes the latest agent comment and the transition into the waiting step", () => {
    const context = readHandoffContext(
      [
        entry({ id: "a", type: "comment", actor_type: "member", content: "Carry on." }),
        entry({ id: "b", type: "comment", content: "Older question" }),
        entry({
          id: "c",
          action: "status_changed",
          details: { from: "in_progress", to: "wait_for_decision" },
        }),
        entry({ id: "d", type: "comment", content: COMMENT }),
        entry({ id: "e", type: "comment", actor_type: "member", content: "Thanks!" }),
      ],
      "wait_for_decision",
    );
    expect(context.comment?.content).toBe(COMMENT);
    expect(context.priorStatus).toBe("in_progress");
  });

  it("says nothing when the timeline has no agent comment or no transition", () => {
    const context = readHandoffContext(
      [entry({ type: "comment", actor_type: "member", content: "hello" })],
      "wait_for_decision",
    );
    expect(context).toEqual({ comment: null, priorStatus: null });
  });

  it("reads the last transition into the waiting step, not an unrelated one", () => {
    const context = readHandoffContext(
      [
        entry({ id: "1", action: "status_changed", details: { from: "todo", to: "in_progress" } }),
        entry({ id: "2", action: "status_changed", details: { from: "in_progress", to: "wait_for_decision" } }),
        entry({ id: "3", action: "status_changed", details: { from: "wait_for_decision", to: "wait_for_decision" } }),
        entry({ id: "4", action: "status_changed", details: { from: "wait_for_decision", to: "in_progress" } }),
        entry({ id: "5", action: "status_changed", details: { from: "in_review", to: "wait_for_decision" } }),
      ],
      "wait_for_decision",
    );
    expect(context.priorStatus).toBe("in_review");
  });
});

describe("handoffDecision", () => {
  const option = { key: "a", label: "Approve", detail: "", nextStatus: null };

  it("sends the chosen option's label", () => {
    expect(handoffDecision(option, "")).toBe("Approve");
    // A choice with a note: the label is the answer, the note is the rationale.
    expect(handoffDecision(option, "and keep the rollback")).toBe("Approve");
  });

  it("sends custom for a written answer, and nothing for an empty form", () => {
    expect(handoffDecision(null, "  Do it after the freeze  ")).toBe("custom");
    expect(handoffDecision(null, "   ")).toBeNull();
  });
});
