import { afterEach, describe, expect, it } from "vitest";
import {
  SIDE_PREVIEW_CSS,
  getSidePreviewTargets,
  publishSidePreview,
  resetSidePreviewTargets,
  subscribeSidePreview,
  withdrawSidePreview,
} from "../src/client/side-preview-store";

function body(): HTMLElement {
  const node = document.createElement("div");
  document.body.append(node);
  return node;
}

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

const issue = (id: string) => ({ view: "issue" as const, id });

afterEach(() => {
  resetSidePreviewTargets();
  document.body.replaceChildren();
});

describe("side preview targets", () => {
  it("publishes a styled surface inside the body's own shadow root", async () => {
    const node = body();
    publishSidePreview(node, issue("iss-1"));
    await flush();

    const [target] = getSidePreviewTargets();
    expect(target).toBeDefined();
    expect(target?.container).toBe(node);
    expect(target?.target).toEqual(issue("iss-1"));
    expect(node.shadowRoot).not.toBeNull();
    expect(target?.surface.parentNode).toBe(node.shadowRoot);
    expect(node.shadowRoot?.querySelector("style")?.textContent).toBe(SIDE_PREVIEW_CSS);
    expect(target?.surface.classList.contains("multica-island-surface")).toBe(true);
  });

  it("republishes the same container for a new target without a second target", async () => {
    const node = body();
    publishSidePreview(node, issue("iss-1"));
    await flush();
    const first = getSidePreviewTargets()[0]!;

    publishSidePreview(node, issue("iss-2"));
    await flush();
    const targets = getSidePreviewTargets();
    expect(targets).toHaveLength(1);
    expect(targets[0]?.key).toBe(first.key);
    expect(targets[0]?.surface).toBe(first.surface);
    expect(targets[0]?.target).toEqual(issue("iss-2"));
  });

  it("swaps params in place for an agent target", async () => {
    const node = body();
    publishSidePreview(node, { view: "agent", id: "agent-1" });
    await flush();
    const first = getSidePreviewTargets()[0]!;

    publishSidePreview(node, { view: "agent", id: "agent-2" });
    await flush();
    const targets = getSidePreviewTargets();
    expect(targets).toHaveLength(1);
    expect(targets[0]?.key).toBe(first.key);
    expect(targets[0]?.target).toEqual({ view: "agent", id: "agent-2" });
  });

  it("keeps one target per container, so two panes show two targets", async () => {
    const left = body();
    const right = body();
    publishSidePreview(left, issue("iss-1"));
    publishSidePreview(right, issue("iss-2"));
    await flush();

    const targets = getSidePreviewTargets();
    expect(targets.map((t) => t.target)).toEqual([issue("iss-1"), issue("iss-2")]);
    expect(new Set(targets.map((t) => t.key)).size).toBe(2);
  });

  it("withdraws on request, and when the tab's lifetime aborts", async () => {
    const node = body();
    const lifetime = new AbortController();
    publishSidePreview(node, issue("iss-1"), lifetime.signal);
    await flush();
    expect(getSidePreviewTargets()).toHaveLength(1);

    lifetime.abort();
    await flush();
    expect(getSidePreviewTargets()).toHaveLength(0);

    publishSidePreview(node, issue("iss-2"));
    await flush();
    withdrawSidePreview(node);
    await flush();
    expect(getSidePreviewTargets()).toHaveLength(0);
  });

  it("withdraws an agent target when the tab's lifetime aborts", async () => {
    const node = body();
    const lifetime = new AbortController();
    publishSidePreview(node, { view: "agent", id: "agent-1" }, lifetime.signal);
    await flush();
    expect(getSidePreviewTargets()).toHaveLength(1);
    lifetime.abort();
    await flush();
    expect(getSidePreviewTargets()).toHaveLength(0);
  });

  it("drops a closed tab's container on the next flush", async () => {
    const node = body();
    publishSidePreview(node, issue("iss-1"));
    await flush();
    node.remove();

    publishSidePreview(body(), null);
    await flush();
    expect(getSidePreviewTargets().map((t) => t.target)).toEqual([null]);
  });

  it("notifies subscribers once per change, with a stable snapshot between them", async () => {
    let notifications = 0;
    const unsubscribe = subscribeSidePreview(() => {
      notifications += 1;
    });
    const before = getSidePreviewTargets();

    publishSidePreview(body(), issue("iss-1"));
    await flush();
    expect(notifications).toBe(1);
    expect(getSidePreviewTargets()).not.toBe(before);

    const after = getSidePreviewTargets();
    expect(getSidePreviewTargets()).toBe(after);
    unsubscribe();
  });
});
