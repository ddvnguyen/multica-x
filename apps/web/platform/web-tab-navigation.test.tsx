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
  window.history.replaceState(null, "", "/acme/issues");
});

function activeTab() {
  const state = useTabStore.getState();
  const group = state.activeWorkspaceSlug
    ? state.byWorkspace[state.activeWorkspaceSlug]
    : undefined;
  return group?.tabs.find((tab) => tab.id === group.activeTabId);
}

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

  // M-413 round 2: the rail's workspace entry, a content link into another
  // workspace, and a click on a tab belonging to another workspace all switch
  // the store group and leave the router to follow. The sync used to refuse to
  // project any URL from a workspace other than the address bar's, so those
  // clicks renamed the tab and left the page where it was.
  it("projects a switch to another workspace", () => {
    act(() => {
      sync(["acme", "beta"]);
    });
    router.replace.mockClear();

    act(() => {
      useTabStore.getState().switchWorkspace("beta", "/beta/projects");
    });

    expect(router.replace).toHaveBeenCalledWith("/beta/projects");
  });

  it("does not project a workspace the user cannot open", () => {
    act(() => {
      sync(["acme"]);
    });
    router.replace.mockClear();

    act(() => {
      useTabStore.getState().switchWorkspace("beta", "/beta/projects");
    });

    expect(router.replace).not.toHaveBeenCalled();
  });

  // A reload with several restored tabs: the address bar names one of them, and
  // that tab has to become the active one. Recording the URL onto whichever tab
  // was persisted as active put the reloaded page in the wrong tab.
  it("activates the restored tab the address bar names", () => {
    act(() => {
      const store = useTabStore.getState();
      store.switchWorkspace("acme");
      store.openTab("/acme/projects", "Projects");
    });
    const before = useTabStore.getState().byWorkspace["acme"]!;
    expect(before.tabs).toHaveLength(2);
    expect(activeTab()?.url).toBe("/acme/issues");

    navState.pathname = "/acme/projects";
    act(() => {
      sync();
    });

    expect(activeTab()?.url).toBe("/acme/projects");
    const after = useTabStore.getState().byWorkspace["acme"]!;
    // The tab that was active kept its own URL instead of being rewritten.
    expect(after.tabs.find((tab) => tab.url === "/acme/issues")).toBeDefined();
  });

  // An in-page anchor (a comment deeplink) changes only the fragment, which
  // `usePathname`/`useSearchParams` do not report. Reading the hash during a
  // render missed it, and the next projection dropped the fragment.
  it("records an in-page anchor in the active tab", () => {
    act(() => {
      sync();
    });

    act(() => {
      window.history.replaceState(null, "", "/acme/issues#comment-1");
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });

    expect(activeTab()?.url).toBe("/acme/issues#comment-1");
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

  it("projects closing the active tab onto the tab that takes its place", () => {
    act(() => {
      sync();
    });
    renderAdapter();
    act(() => {
      nav().openInNewTab?.("/acme/projects", "Projects", { activate: true });
    });
    const closed = activeTab();
    expect(closed?.url).toBe("/acme/projects");

    // Next commits the projection: the address bar now shows the new tab.
    navState.pathname = "/acme/projects";
    resetPendingProjectionForTests();
    act(() => {
      sync();
    });
    router.replace.mockClear();

    act(() => {
      useTabStore.getState().closeTab(closed!.id);
    });

    expect(activeTab()?.url).toBe("/acme/issues");
    expect(router.replace).toHaveBeenCalledWith("/acme/issues");
  });

  // M-480: the dashboard capability's New tab opener is this adapter's
  // `openInNewTab`. Opening the same issue twice must focus the tab that
  // already shows it —
  // the capability's New tab opener relies on the store's `findTabForUrl`
  // dedupe rather than adding a second tab per click.
  it("reuses the tab already showing the issue on a second New tab open", () => {
    act(() => {
      sync();
    });
    renderAdapter();
    act(() => {
      nav().openInNewTab?.("/acme/issues/MUL-1", "MUL-1", { activate: true });
    });
    const first = useTabStore.getState().byWorkspace["acme"]!;
    expect(first.tabs).toHaveLength(2);

    act(() => {
      nav().openInNewTab?.("/acme/issues/MUL-1", "MUL-1", { activate: true });
    });
    const after = useTabStore.getState().byWorkspace["acme"]!;
    expect(after.tabs).toHaveLength(2);
    expect(after.activeTabId).toBe(first.activeTabId);
  });

  // M-413 round 4: Back/Forward are the window's, not the active tab's — the
  // previous location across tabs, focusing the tab that holds it.
  it("backs into the tab holding the previous location", () => {
    act(() => {
      sync();
    });
    renderAdapter();
    act(() => {
      nav().openInNewTab?.("/acme/projects", "Projects", { activate: true });
    });
    const projectsTab = activeTab();
    act(() => {
      nav().push("/acme/projects/p1");
    });

    act(() => {
      nav().back();
    });
    expect(activeTab()?.id).toBe(projectsTab?.id);
    expect(activeTab()?.url).toBe("/acme/projects");

    act(() => {
      nav().back();
    });
    expect(activeTab()?.url).toBe("/acme/issues");
    expect(nav().canGoBack?.()).toBe(false);

    act(() => {
      nav().forward?.();
    });
    expect(activeTab()?.id).toBe(projectsTab?.id);
    expect(activeTab()?.url).toBe("/acme/projects");
  });

  // One session is one tab: a navigation that names a session an open tab
  // already shows focuses it instead of pointing this tab at a second copy.
  it("focuses the tab a chat session already has", () => {
    act(() => {
      sync();
    });
    renderAdapter();
    act(() => {
      nav().openInNewTab?.("/acme/chat?session=a", "hi");
    });
    const chatTab = useTabStore.getState().byWorkspace["acme"]?.tabs[1];
    expect(activeTab()?.url).toBe("/acme/issues");

    act(() => {
      nav().push("/acme/chat?session=a");
    });

    expect(activeTab()?.id).toBe(chatTab?.id);
    expect(useTabStore.getState().byWorkspace["acme"]?.tabs).toHaveLength(2);
    expect(
      useTabStore.getState().byWorkspace["acme"]?.tabs[0]?.url,
    ).toBe("/acme/issues");
  });

  // M-413 round 5: a conversation is a tab of its own. Opening one from a page
  // leaves that page where it is instead of pointing its tab at the chat.
  it("opens a conversation in its own tab instead of retargeting the page", () => {
    act(() => {
      sync();
    });
    renderAdapter();
    const pageTabId = activeTab()?.id;

    act(() => {
      nav().push("/acme/chat?session=a");
    });

    const tabs = useTabStore.getState().byWorkspace["acme"]?.tabs ?? [];
    expect(tabs.map((tab) => tab.url)).toEqual([
      "/acme/issues",
      "/acme/chat?session=a",
    ]);
    expect(activeTab()?.url).toBe("/acme/chat?session=a");
    expect(tabs.find((tab) => tab.id === pageTabId)?.url).toBe("/acme/issues");
  });

  // The Chat page is a container: switching conversations inside it switches
  // the tab's conversation, the way the Inbox rewrites its selection in place.
  it("switches conversations inside the Chat page's own tab", () => {
    act(() => {
      sync();
    });
    renderAdapter();
    act(() => {
      nav().push("/acme/chat?session=a");
    });
    const chatTabId = activeTab()?.id;

    act(() => {
      nav().push("/acme/chat?session=b");
    });

    const chatTabs = (useTabStore.getState().byWorkspace["acme"]?.tabs ?? []).filter(
      (tab) => tab.url.startsWith("/acme/chat"),
    );
    expect(chatTabs).toHaveLength(1);
    expect(activeTab()?.id).toBe(chatTabId);
    expect(activeTab()?.url).toBe("/acme/chat?session=b");
  });

  it("keeps a pinned tab parked and projects the tab an in-app push opens", () => {
    act(() => {
      sync();
    });
    renderAdapter();
    const pinned = useTabStore.getState().byWorkspace["acme"]?.tabs[0];
    act(() => {
      useTabStore.getState().togglePin(pinned!.id);
    });

    act(() => {
      nav().push("/acme/projects");
    });

    const group = useTabStore.getState().byWorkspace["acme"]!;
    expect(group.tabs.find((tab) => tab.id === pinned!.id)?.url).toBe(
      "/acme/issues",
    );
    expect(activeTab()?.url).toBe("/acme/projects");
    expect(router.replace).toHaveBeenCalledWith("/acme/projects");
  });
});
