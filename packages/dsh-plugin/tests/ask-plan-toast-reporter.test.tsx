import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AskPlanToastReporter } from "../src/client/ask-plan-toast-reporter";
import { NavigationProvider } from "../src/fork";
import type { NavigationAdapter } from "../src/fork";

const toastCalls: Array<{ title: unknown; options?: { action?: { label: string } } }> = [];
vi.mock("sonner", () => ({
  toast: (title: unknown, options?: { action?: { label: string } }) => {
    toastCalls.push({ title, options });
  },
}));

const handlers = new Map<string, (payload: unknown) => void>();
vi.mock("../src/fork", async (importOriginal) => {
  const original = await importOriginal<typeof ForkModule>();
  return {
    ...original,
    useWSEvent: (event: string, handler: (payload: unknown) => void) => {
      handlers.set(event, handler);
    },
  };
});

import type * as ForkModule from "../src/fork";

function askItem(overrides: Record<string, unknown> = {}) {
  return {
    item: {
      id: "item-1",
      workspace_id: "ws-1",
      recipient_type: "member",
      recipient_id: "user-1",
      type: "agent_question",
      severity: "action_required",
      issue_id: "issue-1",
      title: "The agent asked you a question",
      body: "Which checkout?",
      ...overrides,
    },
  };
}

const navigation = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/",
  getShareableUrl: (p: string) => p,
} as unknown as NavigationAdapter;

describe("AskPlanToastReporter", () => {
  beforeEach(() => {
    toastCalls.length = 0;
    handlers.clear();
    vi.clearAllMocks();
  });

  it("toasts agent_question rows only, once per item id", () => {
    render(
      <NavigationProvider value={navigation}>
        <AskPlanToastReporter />
      </NavigationProvider>,
    );

    const onInboxNew = handlers.get("inbox:new");
    expect(onInboxNew).toBeDefined();

    onInboxNew!(askItem());
    onInboxNew!(askItem());
    // A comment row is inbox business, not an interruption.
    onInboxNew!({ item: { ...askItem().item, id: "item-2", type: "new_comment" } });
    // plan_review rows toast too.
    onInboxNew!({ item: { ...askItem().item, id: "item-3", type: "plan_review" } });

    expect(toastCalls).toHaveLength(2);
    expect(toastCalls.map((c) => c.options?.action?.label)).toEqual(["Answer", "Review plan"]);
  });
});
