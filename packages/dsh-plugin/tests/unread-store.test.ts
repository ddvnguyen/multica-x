import type { InboxWorkspaceUnread } from "@multica/core/types/inbox";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  attachUnreadBadge,
  getUnread,
  setUnread,
  startUnreadPoll,
  subscribe,
  totalUnreadCount,
} from "../src/client/unread-store";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

/** jsdom's `visibilityState` is a prototype getter; shadow it per test. */
function setVisibility(state: DocumentVisibilityState): void {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => state,
  });
}

describe("totalUnreadCount", () => {
  it("is zero for a missing or empty summary", () => {
    expect(totalUnreadCount(null)).toBe(0);
    expect(totalUnreadCount(undefined)).toBe(0);
    expect(totalUnreadCount([])).toBe(0);
  });

  it("sums every workspace's count", () => {
    expect(
      totalUnreadCount([
        { workspace_id: "ws-1", count: 2 },
        { workspace_id: "ws-2", count: 3 },
        { workspace_id: "ws-3", count: 0 },
      ]),
    ).toBe(5);
  });

  it("ignores rows whose count is not a finite number", () => {
    const rows = [
      { workspace_id: "ws-1", count: 2 },
      { workspace_id: "ws-2", count: "7" },
      { workspace_id: "ws-3", count: Number.NaN },
      { workspace_id: "ws-4", count: 4 },
    ] as unknown as InboxWorkspaceUnread[];
    expect(totalUnreadCount(rows)).toBe(6);
  });
});

describe("unread store", () => {
  beforeEach(() => {
    setUnread(0);
  });

  it("notifies subscribers only when the count changes", () => {
    const seen: number[] = [];
    const unsubscribe = subscribe(() => seen.push(getUnread()));
    setUnread(5);
    setUnread(5);
    setUnread(6);
    unsubscribe();
    expect(seen).toEqual([5, 6]);
  });

  it("stops notifying a listener after it unsubscribes", () => {
    let calls = 0;
    const unsubscribe = subscribe(() => {
      calls += 1;
    });
    unsubscribe();
    setUnread(4);
    expect(calls).toBe(0);
    expect(getUnread()).toBe(4);
  });

  it("clamps negative counts to zero", () => {
    setUnread(4);
    setUnread(-2);
    expect(getUnread()).toBe(0);
  });

  it("paints a badge on attach and hides it on detach", () => {
    setUnread(0);
    const dot = document.createElement("span");
    const detach = attachUnreadBadge(dot);
    expect(dot.style.display).toBe("none");

    setUnread(3);
    expect(dot.style.display).toBe("block");

    detach();
    expect(dot.style.display).toBe("none");

    setUnread(9);
    expect(dot.style.display).toBe("none");
  });
});

describe("unread poller", () => {
  let stopPoll: (() => void) | null = null;

  beforeEach(() => {
    setUnread(0);
    vi.useFakeTimers();
  });

  afterEach(() => {
    stopPoll?.();
    stopPoll = null;
    Reflect.deleteProperty(document, "visibilityState");
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  /** Drain the poller's fetch → parse → publish chain under the fake clock. */
  async function drained(): Promise<void> {
    await vi.advanceTimersByTimeAsync(0);
  }

  it("publishes the summed count from a successful poll", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) =>
      jsonResponse([
        { workspace_id: "ws-1", count: 2 },
        { workspace_id: "ws-2", count: 3 },
      ]),
    );
    vi.stubGlobal("fetch", fetchMock);

    stopPoll = startUnreadPoll({ intervalMs: 60_000 });
    await drained();

    expect(getUnread()).toBe(5);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(url).toBe("/multica/api/inbox/unread-summary");
    expect(init?.credentials).toBe("same-origin");
    expect(init?.headers).toEqual({ accept: "application/json" });
  });

  it("leaves the stored count alone on a non-OK response", async () => {
    setUnread(7);
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, _init?: RequestInit) =>
        jsonResponse({ error: "boom" }, 503),
      ),
    );

    stopPoll = startUnreadPoll({ intervalMs: 60_000 });
    await drained();

    expect(getUnread()).toBe(7);
  });

  it("keeps the stored count when the fetch itself rejects", async () => {
    setUnread(5);
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, _init?: RequestInit): Promise<Response> => {
        throw new Error("network down");
      }),
    );

    stopPoll = startUnreadPoll({ intervalMs: 60_000 });
    await drained();

    expect(getUnread()).toBe(5);
  });

  it("keeps the stored count when the body does not parse to an array", async () => {
    setUnread(4);
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, _init?: RequestInit) =>
        new Response("<html>not json</html>", {
          status: 200,
          headers: { "content-type": "text/html" },
        }),
      ),
    );

    stopPoll = startUnreadPoll({ intervalMs: 60_000 });
    await drained();
    expect(getUnread()).toBe(4);

    // Parsed, but not a summary array: also left alone.
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, _init?: RequestInit) =>
        jsonResponse({ count: 9 }),
      ),
    );
    stopPoll();
    stopPoll = startUnreadPoll({ intervalMs: 60_000 });
    await drained();
    expect(getUnread()).toBe(4);
  });

  it("cancels the interval and the visibility listener when stopped", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) =>
      jsonResponse([]),
    );
    vi.stubGlobal("fetch", fetchMock);

    stopPoll = startUnreadPoll({ intervalMs: 1_000 });
    await drained();
    expect(fetchMock).toHaveBeenCalledTimes(1);

    stopPoll();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(fetchMock).toHaveBeenCalledTimes(1);

    // The visibility listener went with it: showing the tab polls nothing.
    document.dispatchEvent(new Event("visibilitychange"));
    await drained();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("pauses while hidden and refreshes at once when visible again", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) =>
      jsonResponse([]),
    );
    vi.stubGlobal("fetch", fetchMock);

    stopPoll = startUnreadPoll({ intervalMs: 1_000 });
    await drained();
    expect(fetchMock).toHaveBeenCalledTimes(1);

    setVisibility("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(5_000);
    expect(fetchMock).toHaveBeenCalledTimes(1);

    setVisibility("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    await drained();
    expect(fetchMock).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(1_000);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("hands the same stop to a second caller", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse([])),
    );
    const first = startUnreadPoll({ intervalMs: 60_000 });
    stopPoll = first;
    const second = startUnreadPoll({ intervalMs: 60_000 });
    expect(second).toBe(first);
  });
});
