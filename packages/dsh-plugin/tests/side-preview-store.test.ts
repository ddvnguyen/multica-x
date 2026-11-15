import { afterEach, describe, expect, it } from "vitest";
import {
  SIDE_PREVIEW_CSS,
  getSidePreviewTargets,
  publishSidePreview,
  resetSidePreviewTargets,
  subscribeSidePreview,
  withdrawSidePreview,
} from "../src/client/side-preview-store";

/** One body element, in the document like a dsh tab's. */
function body(): HTMLElement {
  const node = document.createElement("div");
  document.body.append(node);
  return node;
}

/** Let the store's microtask flush run. */
async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

afterEach(() => {
  resetSidePreviewTargets();
  document.body.replaceChildren();
});

describe("side preview targets", () => {
  it("publishes a styled surface inside the body's own shadow root", async () => {
    const node = body();
    publishSidePreview(node, "iss-1");
    await flush();

    const [target] = getSidePreviewTargets();
    expect(target).toBeDefined();
    expect(target?.container).toBe(node);
    expect(target?.issueId).toBe("iss-1");
    // The surface is inside a shadow root carrying the island's sheet: a portal
    // into the shell's light DOM would arrive unstyled (popup-seat.ts).
    expect(node.shadowRoot).not.toBeNull();
    expect(target?.surface.parentNode).toBe(node.shadowRoot);
    expect(node.shadowRoot?.querySelector("style")?.textContent).toBe(SIDE_PREVIEW_CSS);
    expect(target?.surface.classList.contains("multica-island-surface")).toBe(true);
  });

  it("republishes the same container for a new issue without a second target", async () => {
    const node = body();
    publishSidePreview(node, "iss-1");
    await flush();
    const first = getSidePreviewTargets()[0]!;

    publishSidePreview(node, "iss-2");
    await flush();
    const targets = getSidePreviewTargets();
    expect(targets).toHaveLength(1);
    // Same portal key and surface: a reveal swaps the content of one pane
    // rather than remounting it (M-336 review condition 2).
    expect(targets[0]?.key).toBe(first.key);
    expect(targets[0]?.surface).toBe(first.surface);
    expect(targets[0]?.issueId).toBe("iss-2");
  });

  it("keeps one target per container, so two panes show two issues", async () => {
    const left = body();
    const right = body();
    publishSidePreview(left, "iss-1");
    publishSidePreview(right, "iss-2");
    await flush();

    const targets = getSidePreviewTargets();
    expect(targets.map((target) => target.issueId)).toEqual(["iss-1", "iss-2"]);
    expect(new Set(targets.map((target) => target.key)).size).toBe(2);
  });

  it("withdraws on request, and when the tab's lifetime aborts", async () => {
    const node = body();
    const lifetime = new AbortController();
    publishSidePreview(node, "iss-1", lifetime.signal);
    await flush();
    expect(getSidePreviewTargets()).toHaveLength(1);

    lifetime.abort();
    await flush();
    expect(getSidePreviewTargets()).toHaveLength(0);

    publishSidePreview(node, "iss-2");
    await flush();
    withdrawSidePreview(node);
    await flush();
    expect(getSidePreviewTargets()).toHaveLength(0);
  });

  it("drops a closed tab's container on the next flush", async () => {
    const node = body();
    publishSidePreview(node, "iss-1");
    await flush();
    node.remove();

    // Any publication is enough to reconcile: the island's own render is what
    // notices, and a closed tab cannot publish again.
    publishSidePreview(body(), null);
    await flush();
    expect(getSidePreviewTargets().map((target) => target.issueId)).toEqual([null]);
  });

  it("notifies subscribers once per change, with a stable snapshot between them", async () => {
    let notifications = 0;
    const unsubscribe = subscribeSidePreview(() => {
      notifications += 1;
    });
    const before = getSidePreviewTargets();

    publishSidePreview(body(), "iss-1");
    await flush();
    expect(notifications).toBe(1);
    expect(getSidePreviewTargets()).not.toBe(before);

    const after = getSidePreviewTargets();
    expect(getSidePreviewTargets()).toBe(after);
    unsubscribe();
  });
});
