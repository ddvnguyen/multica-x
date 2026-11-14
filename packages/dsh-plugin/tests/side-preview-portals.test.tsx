import { act, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactElement } from "react";

// The portal's whole job is where the side stack lands and which issue it
// shows; the stack itself is the fork's and has its own suite.
vi.mock("../src/fork", () => ({
  IssueSideStack: ({ issueId }: { issueId: string }) =>
    createElement("div", { "data-issue": issueId }),
}));

const { SidePreviewPortals } = await import("../src/client/side-preview-portals");
const { createIssuePreviewBody } = await import("../src/client/side-preview-tab");
const store = await import("../src/client/side-preview-store");

/** Let the store's microtask flush reach React. */
async function flush(): Promise<void> {
  await act(async () => {
    // A macrotask, not a microtask: the store notifies from `queueMicrotask`
    // after the body's render, so the flush has to outlast that turn for React
    // to see the update inside act.
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

/** One dsh tab body, rendered by the shell's createElement. */
function mountBody(params: unknown, signal = new AbortController().signal): HTMLElement {
  // The body is built for the shell's `createElement`, whose return type is
  // opaque; React's own element type is what a test can render.
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
});

describe("side preview body", () => {
  it("publishes the container with the issue its params name", async () => {
    const node = mountBody({ issueId: "iss-1" });
    await flush();
    const target = store.getSidePreviewTargets()[0];
    expect(target?.container).toBe(node);
    expect(target?.issueId).toBe("iss-1");
  });

  it("publishes no issue for a tab dsh restored without params", async () => {
    mountBody(undefined);
    await flush();
    expect(store.getSidePreviewTargets()[0]?.issueId).toBeNull();
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

  it("swaps the issue in place when the tab is revealed for another one", async () => {
    const node = mountBody({ issueId: "iss-1" });
    render(createElement(SidePreviewPortals));
    await flush();

    act(() => {
      store.publishSidePreview(node, "iss-2");
    });
    await flush();

    const surface = node.shadowRoot?.querySelector<HTMLElement>(".multica-island-surface");
    expect(surface?.querySelector("[data-issue='iss-2']")).not.toBeNull();
    expect(surface?.querySelector("[data-issue='iss-1']")).toBeNull();
    expect(store.getSidePreviewTargets()).toHaveLength(1);
  });
});
