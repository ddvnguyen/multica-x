import { act, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactElement } from "react";

vi.mock("../src/fork", () => ({
  IssueSideStack: ({ issueId }: { issueId: string }) =>
    createElement("div", { "data-issue": issueId }),
  AgentSideStack: ({ agentId }: { agentId: string }) =>
    createElement("div", { "data-agent": agentId }),
  RuntimeSideStack: ({ runtimeId }: { runtimeId: string }) =>
    createElement("div", { "data-runtime": runtimeId }),
  SkillSideStack: ({ skillId }: { skillId: string }) =>
    createElement("div", { "data-skill": skillId }),
  useCanonicalIssue: () => ({
    issue: { identifier: "M-336", title: "MMX Side preview in dsh right column" },
  }),
  useWorkspaceId: () => "ws-1",
}));

const { SidePreviewPortals } = await import("../src/client/side-preview-portals");
const { createIssuePreviewBody } = await import("../src/client/side-preview-tab");
const store = await import("../src/client/side-preview-store");

async function flush(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

function mountBody(params: unknown, signal = new AbortController().signal): HTMLElement {
  const Body = createIssuePreviewBody(createElement as never) as unknown as (
    props: { useTabInfo: () => unknown },
  ) => ReactElement;
  const useTabInfo = () => ({
    tab: { signal, navigation: { params, revision: 1 } },
  });
  const { container } = render(<Body useTabInfo={useTabInfo} />);
  return container.firstElementChild as HTMLElement;
}

afterEach(() => {
  store.resetSidePreviewTargets();
  localStorage.clear();
});

describe("side preview body", () => {
  it("publishes the container with the issue its params name", async () => {
    const node = mountBody({ issueId: "iss-1" });
    await flush();
    const target = store.getSidePreviewTargets()[0];
    expect(target?.container).toBe(node);
    expect(target?.target).toEqual({ view: "issue", id: "iss-1" });
  });

  it("restores the last target when dsh restores the tab without params", async () => {
    localStorage.setItem(
      "multica.dsh.side-preview-target",
      JSON.stringify({ view: "issue", id: "iss-saved" }),
    );
    mountBody(undefined);
    await flush();
    expect(store.getSidePreviewTargets()[0]?.target).toEqual({ view: "issue", id: "iss-saved" });
  });

  it("publishes no target when dsh restores without params and no saved target", async () => {
    mountBody(undefined);
    await flush();
    expect(store.getSidePreviewTargets()[0]?.target).toBeNull();
  });

  it("withdraws when the tab's lifetime aborts", async () => {
    const lifetime = new AbortController();
    mountBody({ issueId: "iss-1" }, lifetime.signal);
    await flush();
    expect(store.getSidePreviewTargets()).toHaveLength(1);
    lifetime.abort();
    await flush();
    expect(store.getSidePreviewTargets()).toHaveLength(0);
  });

  it("withdraws an agent target when the tab's lifetime aborts", async () => {
    const lifetime = new AbortController();
    mountBody({ view: "agent", id: "agent-1" }, lifetime.signal);
    await flush();
    expect(store.getSidePreviewTargets()).toHaveLength(1);
    lifetime.abort();
    await flush();
    expect(store.getSidePreviewTargets()).toHaveLength(0);
  });

  it("withdraws a runtime target when the tab's lifetime aborts", async () => {
    const lifetime = new AbortController();
    mountBody({ view: "runtime", id: "rt-1" }, lifetime.signal);
    await flush();
    expect(store.getSidePreviewTargets()).toHaveLength(1);
    lifetime.abort();
    await flush();
    expect(store.getSidePreviewTargets()).toHaveLength(0);
  });

  it("withdraws a skill target when the tab's lifetime aborts", async () => {
    const lifetime = new AbortController();
    mountBody({ view: "skill", id: "sk-1" }, lifetime.signal);
    await flush();
    expect(store.getSidePreviewTargets()).toHaveLength(1);
    lifetime.abort();
    await flush();
    expect(store.getSidePreviewTargets()).toHaveLength(0);
  });
});

describe("island portal", () => {
  it("paints the side stack into the tab's surface", async () => {
    const node = mountBody({ issueId: "iss-1" });
    render(createElement(SidePreviewPortals));

    await flush();

    const surface = node.shadowRoot?.querySelector<HTMLElement>(".multica-island-surface");
    expect(surface).not.toBeNull();
    expect(surface?.querySelector("[data-issue='iss-1']")).not.toBeNull();
  });

  it("names the issue in the body header, the chip stays generic", async () => {
    const node = mountBody({ issueId: "iss-1" });
    render(createElement(SidePreviewPortals));
    await flush();

    const surface = node.shadowRoot?.querySelector<HTMLElement>(".multica-island-surface");
    const header = surface?.querySelector("[data-multica-side-preview-header]");
    expect(header?.textContent).toContain("M-336");
    expect(header?.textContent).toContain("MMX Side preview in dsh right column");
  });

  it("swaps the issue in place when the tab is revealed for another one", async () => {
    const node = mountBody({ issueId: "iss-1" });
    render(createElement(SidePreviewPortals));
    await flush();

    act(() => {
      store.publishSidePreview(node, { view: "issue", id: "iss-2" });
    });
    await flush();

    const surface = node.shadowRoot?.querySelector<HTMLElement>(".multica-island-surface");
    expect(surface?.querySelector("[data-issue='iss-2']")).not.toBeNull();
    expect(surface?.querySelector("[data-issue='iss-1']")).toBeNull();
    expect(store.getSidePreviewTargets()).toHaveLength(1);
  });

  it("paints the agent side stack for an agent target", async () => {
    const node = mountBody({ view: "agent", id: "agent-1" });
    render(createElement(SidePreviewPortals));
    await flush();

    const surface = node.shadowRoot?.querySelector<HTMLElement>(".multica-island-surface");
    expect(surface?.querySelector("[data-agent='agent-1']")).not.toBeNull();
    expect(surface?.textContent).not.toContain("Preview unavailable");
  });

  it("swaps the agent in place when the tab is revealed for another one", async () => {
    const node = mountBody({ view: "agent", id: "agent-1" });
    render(createElement(SidePreviewPortals));
    await flush();

    act(() => {
      store.publishSidePreview(node, { view: "agent", id: "agent-2" });
    });
    await flush();

    const surface = node.shadowRoot?.querySelector<HTMLElement>(".multica-island-surface");
    expect(surface?.querySelector("[data-agent='agent-2']")).not.toBeNull();
    expect(surface?.querySelector("[data-agent='agent-1']")).toBeNull();
    expect(store.getSidePreviewTargets()).toHaveLength(1);
  });

  it("paints the runtime side stack for a runtime target", async () => {
    const node = mountBody({ view: "runtime", id: "rt-1" });
    render(createElement(SidePreviewPortals));
    await flush();

    const surface = node.shadowRoot?.querySelector<HTMLElement>(".multica-island-surface");
    expect(surface?.querySelector("[data-runtime='rt-1']")).not.toBeNull();
    expect(surface?.textContent).not.toContain("Preview unavailable");
  });

  it("swaps the runtime in place when the tab is revealed for another one", async () => {
    const node = mountBody({ view: "runtime", id: "rt-1" });
    render(createElement(SidePreviewPortals));
    await flush();

    act(() => {
      store.publishSidePreview(node, { view: "runtime", id: "rt-2" });
    });
    await flush();

    const surface = node.shadowRoot?.querySelector<HTMLElement>(".multica-island-surface");
    expect(surface?.querySelector("[data-runtime='rt-2']")).not.toBeNull();
    expect(surface?.querySelector("[data-runtime='rt-1']")).toBeNull();
    expect(store.getSidePreviewTargets()).toHaveLength(1);
  });

  it("paints the skill side stack for a skill target", async () => {
    const node = mountBody({ view: "skill", id: "sk-1" });
    render(createElement(SidePreviewPortals));
    await flush();

    const surface = node.shadowRoot?.querySelector<HTMLElement>(".multica-island-surface");
    expect(surface?.querySelector("[data-skill='sk-1']")).not.toBeNull();
    expect(surface?.textContent).not.toContain("Preview unavailable");
  });

  it("swaps the skill in place when the tab is revealed for another one", async () => {
    const node = mountBody({ view: "skill", id: "sk-1" });
    render(createElement(SidePreviewPortals));
    await flush();

    act(() => {
      store.publishSidePreview(node, { view: "skill", id: "sk-2" });
    });
    await flush();

    const surface = node.shadowRoot?.querySelector<HTMLElement>(".multica-island-surface");
    expect(surface?.querySelector("[data-skill='sk-2']")).not.toBeNull();
    expect(surface?.querySelector("[data-skill='sk-1']")).toBeNull();
    expect(store.getSidePreviewTargets()).toHaveLength(1);
  });
});
