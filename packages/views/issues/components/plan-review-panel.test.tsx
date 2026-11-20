import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@multica/core/api";
import type { AgentPlan } from "@multica/core/issues/ask-plan";
import { renderWithI18n } from "../../test/i18n";
import { PlanReviewPanel } from "./plan-review-panel";

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = (await importOriginal()) as Record<string, unknown>;
  return {
    ...actual,
    api: {
      addPlanComment: vi.fn(),
      getPlanRevision: vi.fn(),
      resolvePlanComment: vi.fn(),
      reviewPlan: vi.fn(),
      writePlanSection: vi.fn(),
      listPlanSectionVersions: vi.fn(),
    },
  };
});
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getActorName: () => "Ada",
    hasActor: () => true,
  }),
}));
vi.mock("../../editor", () => ({
  ReadonlyContent: ({ content }: { content: string }) => <p>{content}</p>,
}));

const PLAN: AgentPlan = {
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
  comments: [],
};

const COMMENTED: AgentPlan = {
  ...PLAN,
  comments: [
    {
      id: "comment-root",
      plan_id: "plan-4",
      section_id: "s1",
      quote: "Two lanes, z first.",
      body: "Start with the smaller lane.",
      parent_comment_id: null,
      author_user_id: "member-1",
      created_at: "2026-11-07T10:00:00Z",
    },
    {
      id: "comment-reply",
      plan_id: "plan-4",
      section_id: "s1",
      quote: "",
      body: "Agreed, z first.",
      parent_comment_id: "comment-root",
      author_user_id: "member-1",
      created_at: "2026-11-07T10:05:00Z",
    },
  ],
};

function renderPanel(plan: AgentPlan = PLAN) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const view = renderWithI18n(
    <QueryClientProvider client={client}>
      <PlanReviewPanel plan={plan} issueId="issue-3" />
    </QueryClientProvider>,
  );
  return { ...view, client };
}

/** Select a section's rendered text the way a member drags over it. */
function selectPassage(sectionId: string): void {
  const body = document.querySelector(
    `[data-plan-section="${sectionId}"] [data-plan-section-body]`,
  );
  const target = body?.querySelector("p") ?? body;
  if (target === null || target === undefined) throw new Error("no section body");
  const range = document.createRange();
  range.selectNodeContents(target);
  const selection = window.getSelection();
  if (selection === null) throw new Error("no selection");
  selection.removeAllRanges();
  selection.addRange(range);
  fireEvent(document, new Event("selectionchange"));
  // A drag ends on pointer-up: the tooltip raises from the settled range,
  // not from each intermediate selectionchange.
  fireEvent(document, new Event("pointerup"));
}

afterEach(() => {
  vi.clearAllMocks();
});

describe("PlanReviewPanel", () => {
  it("turns a selected passage into an anchored comment", async () => {
    vi.mocked(api.addPlanComment).mockResolvedValue({ comment: COMMENTED.comments![0]! });
    renderPanel();

    expect(await screen.findByText("Split the cutover")).toBeInTheDocument();
    selectPassage("s1");

    // The tooltip raises at the selection; opening it anchors the composer
    // with the quoted passage.
    fireEvent.click(await screen.findByRole("button", { name: "Comment" }));
    expect(await screen.findByText("“Two lanes, z first.”")).toBeInTheDocument();
    fireEvent.change(screen.getByRole("textbox", { name: "What should change here?" }), {
      target: { value: "Start with the smaller lane." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Comment" }));

    await waitFor(() =>
      expect(api.addPlanComment).toHaveBeenCalledWith("ws-1", "plan-4", {
        base_revision: 6,
        section_id: "s1",
        quote: "Two lanes, z first.",
        body: "Start with the smaller lane.",
      }),
    );
    // The saved thread opens below the plan with its new highlight target.
    expect(await screen.findByRole("button", { name: "Hide comments" })).toBeInTheDocument();
  });

  it("threads a reply under its root and resolves the thread", async () => {
    vi.mocked(api.addPlanComment).mockResolvedValue({ comment: COMMENTED.comments![1]! });
    vi.mocked(api.resolvePlanComment).mockResolvedValue({
      comment: { ...COMMENTED.comments![0]!, resolved_at: "2026-11-07T11:00:00Z" },
    });
    renderPanel(COMMENTED);

    // The thread list sits below the plan, closed until the member opens it.
    fireEvent.click(await screen.findByRole("button", { name: /Show comments/ }));
    expect(await screen.findByText("Start with the smaller lane.")).toBeInTheDocument();
    // The reply renders in the same thread, indented under its root.

    fireEvent.click(screen.getByRole("button", { name: "Reply" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Reply to this comment" }), {
      target: { value: "Then the bigger one." },
    });
    fireEvent.keyDown(screen.getByRole("textbox", { name: "Reply to this comment" }), {
      key: "Enter",
    });

    await waitFor(() =>
      expect(api.addPlanComment).toHaveBeenCalledWith("ws-1", "plan-4", {
        base_revision: 6,
        section_id: "s1",
        quote: "Two lanes, z first.",
        body: "Then the bigger one.",
        parent_comment_id: "comment-root",
      }),
    );

    fireEvent.click(screen.getByRole("button", { name: "Resolve" }));
    await waitFor(() =>
      expect(api.resolvePlanComment).toHaveBeenCalledWith(
        "ws-1",
        "plan-4",
        "comment-root",
      ),
    );
  });

  it("reports a stale revision instead of claiming the comment landed", async () => {
    vi.mocked(api.addPlanComment).mockRejectedValue(
      new ApiError("plan changed since this revision", 409, "Conflict"),
    );
    const onRejected = vi.fn();
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    renderWithI18n(
      <QueryClientProvider client={client}>
        <PlanReviewPanel plan={PLAN} issueId="issue-3" onRejected={onRejected} />
      </QueryClientProvider>,
    );

    expect(await screen.findByText("Split the cutover")).toBeInTheDocument();
    selectPassage("s1");
    fireEvent.click(await screen.findByRole("button", { name: "Comment" }));
    fireEvent.change(screen.getByRole("textbox", { name: "What should change here?" }), {
      target: { value: "Start with the smaller lane." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Comment" }));

    expect(await screen.findByRole("status")).toHaveTextContent(
      "The plan moved since you opened it.",
    );
    expect(onRejected).toHaveBeenCalled();
    // The draft stays so the member can re-send it on the new revision.
    expect(screen.getByRole("textbox", { name: "What should change here?" })).toHaveValue(
      "Start with the smaller lane.",
    );
  });

  it("shows every quote in the thread list when the highlight API is unavailable", async () => {
    renderPanel(COMMENTED);
    fireEvent.click(await screen.findByRole("button", { name: /Show comments/ }));
    expect(await screen.findByText("Start with the smaller lane.")).toBeInTheDocument();
    // jsdom has no CSS Custom Highlight API: the paint is skipped, the quote
    // is still readable in the thread's blockquote.
    const quotes = Array.from(document.querySelectorAll("blockquote")).map(
      (node) => node.textContent,
    );
    expect(quotes).toContain("Two lanes, z first.");
    expect(screen.getByText("Start with the smaller lane.")).toBeInTheDocument();
  });

  it("saves a section edit fenced on the page's revision", async () => {
    vi.mocked(api.writePlanSection).mockResolvedValue({ ...PLAN, revision: 7 });
    renderPanel();

    expect(await screen.findByText("Split the cutover")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Edit section Scope" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Section text" }), {
      target: { value: "Two lanes, z first, then web." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save section" }));

    await waitFor(() =>
      expect(api.writePlanSection).toHaveBeenCalledWith("ws-1", "plan-4", "s1", {
        base_revision: 6,
        heading: "Scope",
        markdown: "Two lanes, z first, then web.",
      }),
    );
  });

  it("keeps request changes back until a passage comment is waiting", async () => {
    renderPanel(COMMENTED);
    fireEvent.click(await screen.findByRole("button", { name: /Show comments/ }));
    expect(await screen.findByText("Start with the smaller lane.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Request changes" })).toBeEnabled();
  });

  it("labels each thread with the revision it was made on", async () => {
    const REVISED: AgentPlan = {
      ...COMMENTED,
      comments: COMMENTED.comments!.map((comment) => ({ ...comment, revision: 6 })),
    };
    renderPanel(REVISED);
    fireEvent.click(await screen.findByRole("button", { name: /Show comments/ }));
    expect(await screen.findByText("R6")).toBeInTheDocument();
  });

  it("reads an older revision read-only and returns to current", async () => {
    vi.mocked(api.getPlanRevision).mockResolvedValue({
      plan_id: "plan-4",
      revision: 5,
      current: 6,
      status: "in_review",
      sections: [
        { section_id: "s1", heading: "Scope", markdown: "Two lanes.", available: true },
        { section_id: "s2", heading: "Gone", markdown: "", available: false },
      ],
    });
    renderPanel();
    expect(await screen.findByText("Split the cutover")).toBeInTheDocument();

    // The switcher lists every revision; the current one is marked.
    fireEvent.change(screen.getByRole("combobox", { name: "Show another revision" }), {
      target: { value: "5" },
    });

    // The banner names the old revision; its text reads, with a placeholder
    // where the ledger has nothing; verdicts and edits are gone.
    expect(await screen.findByRole("status")).toHaveTextContent(
      "Viewing revision 5 of 6",
    );
    expect(await screen.findByText("Two lanes.")).toBeInTheDocument();
    expect(
      screen.getByText("Gone at revision 5 predates the recorded history."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve plan" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit section Scope" })).not.toBeInTheDocument();
    await waitFor(() =>
      expect(api.getPlanRevision).toHaveBeenCalledWith("ws-1", "plan-4", 5),
    );

    fireEvent.click(screen.getByRole("button", { name: "Back to current" }));
    expect(await screen.findByText("Two lanes, z first.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve plan" })).toBeInTheDocument();
  });

  it("opens a thread when its painted passage is clicked", async () => {
    const REVISED: AgentPlan = {
      ...COMMENTED,
      comments: COMMENTED.comments!.map((comment) => ({ ...comment, revision: 6 })),
    };
    renderPanel(REVISED);
    expect(await screen.findByText("Split the cutover")).toBeInTheDocument();

    // Hit-test the click inside the quoted passage: jsdom ranges have no
    // rects, so every range reports the clicked point's rect.
    const clicked = [{ left: 0, top: 0, right: 100, bottom: 20 }] as DOMRect[];
    const proto = Range.prototype as Range & {
      getClientRects?: () => DOMRectList;
    };
    const previous = proto.getClientRects;
    proto.getClientRects = () => clicked as unknown as DOMRectList;
    try {
      const target = document.querySelector(
        '[data-plan-section="s1"] [data-plan-section-body] p',
      );
      if (target == null) throw new Error("no section text");
      fireEvent.click(target, { clientX: 10, clientY: 10 });
    } finally {
      if (previous === undefined) Reflect.deleteProperty(proto, "getClientRects");
      else proto.getClientRects = previous;
    }

    // The thread list opened on the matching thread.
    expect(await screen.findByRole("button", { name: "Hide comments" })).toBeInTheDocument();
    expect(await screen.findByText("Start with the smaller lane.")).toBeInTheDocument();
  });

  it("raises the tooltip inside a shadow root", async () => {
    vi.mocked(api.addPlanComment).mockResolvedValue({ comment: COMMENTED.comments![0]! });
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    // The dsh plugin renders the plan inside a shadow root: the document
    // selection stays collapsed and retargeted, so the panel must read the
    // root's own selection.
    const host = document.createElement("div");
    document.body.appendChild(host);
    const shadow = host.attachShadow({ mode: "open" });
    const queries = within(shadow as unknown as HTMLElement);
    try {
      renderWithI18n(
        <QueryClientProvider client={client}>
          <PlanReviewPanel plan={PLAN} issueId="issue-3" />
        </QueryClientProvider>,
        { container: shadow as unknown as HTMLElement },
      );
      const body = shadow.querySelector(
        '[data-plan-section="s1"] [data-plan-section-body]',
      );
      const target = body?.querySelector("p") ?? null;
      if (target == null) throw new Error("no section body");
      const range = document.createRange();
      range.selectNodeContents(target);
      const removeAllRanges = vi.fn();
      const shadowSelection = {
        isCollapsed: false,
        rangeCount: 1,
        anchorNode: target.firstChild,
        focusNode: target.firstChild,
        toString: () => "Two lanes, z first.",
        getRangeAt: () => range,
        removeAllRanges,
      };
      (shadow as unknown as Record<string, unknown>)["getSelection"] = () => shadowSelection;
      shadow.dispatchEvent(new Event("selectionchange"));
      shadow.dispatchEvent(new Event("pointerup"));

      // The tooltip raises from the root's selection, not the document's.
      const tooltip = await queries.findByRole("button", { name: "Comment" });
      fireEvent.click(tooltip);
      expect(await queries.findByText("“Two lanes, z first.”")).toBeInTheDocument();
      fireEvent.change(queries.getByRole("textbox", { name: "What should change here?" }), {
        target: { value: "Start with the smaller lane." },
      });
      fireEvent.click(queries.getByRole("button", { name: "Comment" }));

      await waitFor(() =>
        expect(api.addPlanComment).toHaveBeenCalledWith("ws-1", "plan-4", {
          base_revision: 6,
          section_id: "s1",
          quote: "Two lanes, z first.",
          body: "Start with the smaller lane.",
        }),
      );
      // Clearing reaches the shadow tree's own selection.
      expect(removeAllRanges).toHaveBeenCalled();
    } finally {
      host.remove();
    }
  });

  it("opens a thread from a click inside a shadow root", async () => {
    const REVISED: AgentPlan = {
      ...COMMENTED,
      comments: COMMENTED.comments!.map((comment) => ({ ...comment, revision: 6 })),
    };
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    const host = document.createElement("div");
    document.body.appendChild(host);
    const shadow = host.attachShadow({ mode: "open" });
    const queries = within(shadow as unknown as HTMLElement);
    try {
      renderWithI18n(
        <QueryClientProvider client={client}>
          <PlanReviewPanel plan={REVISED} issueId="issue-3" />
        </QueryClientProvider>,
        { container: shadow as unknown as HTMLElement },
      );
      expect(await queries.findByText("Split the cutover")).toBeInTheDocument();
      const clicked = [{ left: 0, top: 0, right: 100, bottom: 20 }] as DOMRect[];
      const proto = Range.prototype as Range & {
        getClientRects?: () => DOMRectList;
      };
      const previous = proto.getClientRects;
      proto.getClientRects = () => clicked as unknown as DOMRectList;
      try {
        const target = shadow.querySelector(
          '[data-plan-section="s1"] [data-plan-section-body] p',
        );
        if (target == null) throw new Error("no section text");
        fireEvent.click(target, { clientX: 10, clientY: 10 });
      } finally {
        if (previous === undefined) Reflect.deleteProperty(proto, "getClientRects");
        else proto.getClientRects = previous;
      }
      expect(await queries.findByRole("button", { name: "Hide comments" })).toBeInTheDocument();
      expect(await queries.findByText("Start with the smaller lane.")).toBeInTheDocument();
    } finally {
      host.remove();
    }
  });

  it("saves the settled range, not each intermediate pick", async () => {
    vi.mocked(api.addPlanComment).mockResolvedValue({ comment: COMMENTED.comments![0]! });
    renderPanel();
    expect(await screen.findByText("Split the cutover")).toBeInTheDocument();

    // A slow drag fires selectionchange on the first characters ("Two"),
    // then extends to the full passage before pointer-up. The saved quote
    // must be the settled range, and the tooltip button must not steal
    // focus mid-drag (which would freeze the range at its first characters).
    const body = document.querySelector(
      '[data-plan-section="s1"] [data-plan-section-body]',
    );
    const target = body?.querySelector("p");
    if (target?.firstChild == null) throw new Error("no section text");
    const text = target.firstChild;
    const selection = window.getSelection();
    if (selection === null) throw new Error("no selection");
    const partial = document.createRange();
    partial.setStart(text, 0);
    partial.setEnd(text, 3);
    selection.removeAllRanges();
    selection.addRange(partial);
    fireEvent(document, new Event("selectionchange"));
    // No tooltip yet: the pick has not settled.
    expect(screen.queryByRole("button", { name: "Comment" })).not.toBeInTheDocument();

    const full = document.createRange();
    full.selectNodeContents(target);
    selection.removeAllRanges();
    selection.addRange(full);
    fireEvent(document, new Event("pointerup"));

    fireEvent.click(await screen.findByRole("button", { name: "Comment" }));
    expect(await screen.findByText("“Two lanes, z first.”")).toBeInTheDocument();
    expect(document.activeElement?.tagName).not.toBe("BUTTON");
    fireEvent.change(screen.getByRole("textbox", { name: "What should change here?" }), {
      target: { value: "Start with the smaller lane." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Comment" }));

    await waitFor(() =>
      expect(api.addPlanComment).toHaveBeenCalledWith("ws-1", "plan-4", {
        base_revision: 6,
        section_id: "s1",
        quote: "Two lanes, z first.",
        body: "Start with the smaller lane.",
      }),
    );
  });
});
