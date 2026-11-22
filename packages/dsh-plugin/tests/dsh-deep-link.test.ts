import { describe, expect, it, vi } from "vitest";
import {
  DSH_SESSION_PARAM,
  followDshSessionDeepLink,
  readDshSessionParam,
  withoutDshSessionParam,
  type DeepLinkSessions,
} from "../src/client/dsh-deep-link";

/** One session-list snapshot, with the two members the consumer reads. */
interface Snapshot {
  byId: Record<string, unknown>;
  phase?: string;
}

/** A fake `sessions.list`: tests publish snapshots the way dsh does. */
function fakeSessions(initial: Snapshot) {
  let snapshot = initial;
  const listeners = new Set<() => void>();
  const sessions: DeepLinkSessions = {
    list: {
      getSnapshot: () => snapshot,
      subscribe: (listener) => {
        listeners.add(listener);
        return () => { listeners.delete(listener); };
      },
    },
  };
  return {
    sessions,
    publish(next: Snapshot) {
      snapshot = next;
      for (const listener of [...listeners]) listener();
    },
    get listenerCount() {
      return listeners.size;
    },
  };
}

describe("readDshSessionParam", () => {
  it("reads the parameter", () => {
    expect(readDshSessionParam(`?${DSH_SESSION_PARAM}=ses-abc`)).toBe("ses-abc");
  });

  it("decodes what the server encoded", () => {
    expect(readDshSessionParam(`?${DSH_SESSION_PARAM}=a%2Fb`)).toBe("a/b");
  });

  it("treats a blank value as absent", () => {
    expect(readDshSessionParam(`?${DSH_SESSION_PARAM}=%20%20`)).toBeNull();
    expect(readDshSessionParam(`?${DSH_SESSION_PARAM}=`)).toBeNull();
  });

  it("is quiet when the parameter is missing", () => {
    expect(readDshSessionParam("")).toBeNull();
    expect(readDshSessionParam("?theme=dark")).toBeNull();
  });
});

describe("withoutDshSessionParam", () => {
  it("drops the parameter and keeps the rest", () => {
    expect(withoutDshSessionParam(`?${DSH_SESSION_PARAM}=ses-abc&theme=dark`)).toBe("?theme=dark");
  });

  it("keeps an empty search empty", () => {
    expect(withoutDshSessionParam(`?${DSH_SESSION_PARAM}=ses-abc`)).toBe("");
  });
});

describe("followDshSessionDeepLink", () => {
  it("selects a session the shell already lists", () => {
    const store = fakeSessions({ byId: { "ses-abc": {} }, phase: "ready" });
    const openSession = vi.fn();
    const replaceSearch = vi.fn();

    const pending = followDshSessionDeepLink({
      search: `?theme=dark&${DSH_SESSION_PARAM}=ses-abc`,
      sessions: store.sessions,
      navigation: { openSession },
      replaceSearch,
    });

    expect(pending).toBeNull();
    expect(openSession).toHaveBeenCalledExactlyOnceWith("ses-abc");
    expect(replaceSearch).toHaveBeenCalledExactlyOnceWith("?theme=dark");
    expect(store.listenerCount).toBe(0);
  });

  it("waits for a session the first pull has not delivered yet", () => {
    const store = fakeSessions({ byId: {}, phase: "pending" });
    const openSession = vi.fn();

    followDshSessionDeepLink({
      search: `?${DSH_SESSION_PARAM}=ses-later`,
      sessions: store.sessions,
      navigation: { openSession },
      replaceSearch: vi.fn(),
    });

    expect(openSession).not.toHaveBeenCalled();
    expect(store.listenerCount).toBe(1);

    store.publish({ byId: {}, phase: "pending" });
    expect(openSession).not.toHaveBeenCalled();

    store.publish({ byId: { "ses-later": {} }, phase: "ready" });
    expect(openSession).toHaveBeenCalledExactlyOnceWith("ses-later");
    expect(store.listenerCount).toBe(0);
  });

  it("gives up once a ready list does not carry the session", () => {
    const store = fakeSessions({ byId: {}, phase: "pending" });
    const openSession = vi.fn();

    followDshSessionDeepLink({
      search: `?${DSH_SESSION_PARAM}=ses-elsewhere`,
      sessions: store.sessions,
      navigation: { openSession },
      replaceSearch: vi.fn(),
    });

    store.publish({ byId: { "ses-other": {} }, phase: "ready" });

    expect(openSession).not.toHaveBeenCalled();
    expect(store.listenerCount).toBe(0);
  });

  it("can be disposed while it waits", () => {
    const store = fakeSessions({ byId: {}, phase: "pending" });
    const openSession = vi.fn();

    const pending = followDshSessionDeepLink({
      search: `?${DSH_SESSION_PARAM}=ses-later`,
      sessions: store.sessions,
      navigation: { openSession },
      replaceSearch: vi.fn(),
    });
    pending?.();

    store.publish({ byId: { "ses-later": {} }, phase: "ready" });
    expect(openSession).not.toHaveBeenCalled();
    expect(store.listenerCount).toBe(0);
  });

  it("does nothing without a parameter, and leaves the URL alone", () => {
    const store = fakeSessions({ byId: { "ses-abc": {} }, phase: "ready" });
    const openSession = vi.fn();
    const replaceSearch = vi.fn();

    const pending = followDshSessionDeepLink({
      search: "?theme=dark",
      sessions: store.sessions,
      navigation: { openSession },
      replaceSearch,
    });

    expect(pending).toBeNull();
    expect(openSession).not.toHaveBeenCalled();
    expect(replaceSearch).not.toHaveBeenCalled();
  });

  it("consumes the parameter on a host that cannot navigate", () => {
    const store = fakeSessions({ byId: { "ses-abc": {} }, phase: "ready" });
    const replaceSearch = vi.fn();

    const pending = followDshSessionDeepLink({
      search: `?${DSH_SESSION_PARAM}=ses-abc`,
      sessions: store.sessions,
      navigation: null,
      replaceSearch,
    });

    expect(pending).toBeNull();
    expect(replaceSearch).toHaveBeenCalledExactlyOnceWith("");
  });

  it("consumes the parameter on a host that publishes no session list", () => {
    const openSession = vi.fn();
    const replaceSearch = vi.fn();

    const pending = followDshSessionDeepLink({
      search: `?${DSH_SESSION_PARAM}=ses-abc`,
      sessions: null,
      navigation: { openSession },
      replaceSearch,
    });

    expect(pending).toBeNull();
    expect(openSession).not.toHaveBeenCalled();
    expect(replaceSearch).toHaveBeenCalledExactlyOnceWith("");
  });
});
