/**
 * M-388 S4 — web tab router sync and adapter over the shared tab store.
 *
 * The store is the same `@multica/core/tabs` module desktop uses; these
 * tests drive the web half (Next router sync + WebTabNavigationProvider)
 * with a mocked `next/navigation` router.
 */
import { describe, expect, it, vi, beforeEach } from "vitest";
import { act, render } from "@testing-library/react";

const router = vi.hoisted(() => ({
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  forward: vi.fn(),
  prefetch: vi.fn(),
}));

const navState = vi.hoisted(() => ({
  pathname: "/acme/issues",
  search: "",
}));

vi.mock("next/navigation", () => ({
  useRouter: () => router,
  usePathname: () => navState.pathname,
  useSearchParams: () => new URLSearchParams(navState.search),
}));

import { useTabStore, type TabSession } from "@multica/core/tabs";
import { useNavigation } from "@multica/views/navigation";
import {
  WebTabNavigationProvider,
  useWebTabSync,
  routeWebContentLinkPath,
  resetPendingProjectionForTests,
} from "./web-tab-navigation";

function resetStore(): void {
  useTabStore.setState({ activeWorkspaceSlug: null, byWorkspace: {} });
  localStorage.clear();
}

function renderAdapter(): void {
  function Probe() {
    const navigation = useNavigation();
    globalThis.__testNav = navigation;
    return null;
  }
  render(
    <WebTabNavigationProvider>
      <Probe />
    </WebTabNavigationProvider>,
  );
}

interface TestNav {
  push: (p: string) => void;
  replace: (p: string) => void;
  back: () => void;
  forward?: () => void;
  canGoBack?: () => boolean;
  openInNewTab?: (p: string, t?: string, o?: { activate?: boolean }) => void;
}

declare global {
  var __testNav: TestNav | undefined;
}

function nav(): TestNav {
  const current = globalThis.__testNav;
  if (!current) throw new Error("navigation not rendered");
  return current;
}

function sync(validSlugs: readonly string[] = ["acme"]): void {
  function Harness() {
    useWebTabSync({ validSlugs });
    return null;
  }
  // One tree across rerenders: separate renders unmount the sync, and the
  // fresh projection guard treats the store change as initialization.
  const { rerender } = render(<Harness />);
  rerender(<Harness />);
}

beforeEach(() => {
  resetStore();
  resetPendingProjectionForTests();
  router.push.mockReset();
  router.replace.mockReset();
  router.back.mockReset();
  navState.pathname = "/acme/issues";
  navState.search = "";
});

describe("web tab router sync", () => {
  it("seeds a default tab for a workspace with no group", () => {
    act(() => {
      sync();
    });
    const state = useTabStore.getState();
    expect(state.activeWorkspaceSlug).toBe("acme");
    const tabs = state.byWorkspace["acme"]?.tabs ?? [];
    expect(tabs).toHaveLength(1);
    expect(tabs[0]?.url).toBe("/acme/issues");
  });

  it("records router navigations on the active session", () => {
    act(() => {
      sync();
    });
    navState.pathname = "/acme/projects";
    act(() => {
      sync();
    });
    const active = useTabStore.getState().byWorkspace["acme"]?.tabs[0];
    expect(active?.url).toBe("/acme/projects");
    expect(active?.history.stack).toContain("/acme/projects");
  });

  it("projects active-session changes onto the router with replace", () => {
    act(() => {
      sync();
    });
    act(() => {
      useTabStore.getState().navigateActiveSession("/acme/inbox");
    });
    expect(router.replace).toHaveBeenCalledWith("/acme/inbox");
  });
});

describe("WebTabNavigationProvider adapter", () => {
  it("push writes through the session and canGoBack reads the stack", () => {
    act(() => {
      sync();
    });
    renderAdapter();
    act(() => {
      nav().push("/acme/projects");
    });
    expect(useTabStore.getState().byWorkspace["acme"]?.tabs[0]?.url).toBe(
      "/acme/projects",
    );
    expect(nav().canGoBack?.()).toBe(true);
    act(() => {
      nav().back();
    });
    expect(useTabStore.getState().byWorkspace["acme"]?.tabs[0]?.url).toBe(
      "/acme/issues",
    );
  });

  // M-413: a row/link click reaches the adapter's `push`, which writes the
  // session. The sync is what moves the Next router, so a push that never got
  // projected showed the new tab name over the old page — the agent row click
  // that "did not open" the agent.
  it("projects an adapter push onto the router", () => {
    act(() => {
      sync();
    });
    renderAdapter();
    act(() => {
      nav().push("/acme/agents/agent-1");
    });
    expect(router.replace).toHaveBeenCalledWith("/acme/agents/agent-1");
  });

  it("projects an adapter replace onto the router", () => {
    act(() => {
      sync();
    });
    renderAdapter();
    act(() => {
      nav().replace("/acme/agents/agent-1?view=general");
    });
    expect(router.replace).toHaveBeenCalledWith("/acme/agents/agent-1?view=general");
  });

  it("openInNewTab opens a background tab by default", () => {
    act(() => {
      sync();
    });
    renderAdapter();
    act(() => {
      nav().openInNewTab?.("/acme/inbox");
    });
    const tabs = useTabStore.getState().byWorkspace["acme"]?.tabs ?? [];
    expect(tabs).toHaveLength(2);
    // Background: active tab keeps focus.
    expect(useTabStore.getState().byWorkspace["acme"]?.activeTabId).toBe(
      tabs[0]?.id,
    );
  });

  it("content links navigate in place for plain clicks", () => {
    act(() => {
      sync();
    });
    routeWebContentLinkPath("/acme/inbox", "push");
    expect(useTabStore.getState().byWorkspace["acme"]?.tabs[0]?.url).toBe(
      "/acme/inbox",
    );
  });
});
