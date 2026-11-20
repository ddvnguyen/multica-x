import { act, fireEvent, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useTabStore } from "@multica/core/tabs";
import { NavigationProvider, type NavigationAdapter } from "../navigation";
import { renderWithI18n } from "../test/i18n";
import { TabHistoryButtons } from "./tab-history-buttons";

// Same contract the web adapter implements: Back and Forward step the active
// session's virtual stack, and `canGoBack` adds the page behind the shell.
const adapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(() => useTabStore.getState().goBack()),
  // The session stack is what the buttons read; the adapter's own answer is
  // only the fallback for the page behind the shell.
  canGoBack: vi.fn(() => false),
  forward: vi.fn(() => useTabStore.getState().goForward()),
};

function nav(): NavigationAdapter {
  return {
    push: adapter.push,
    replace: adapter.replace,
    back: adapter.back,
    canGoBack: adapter.canGoBack,
    forward: adapter.forward,
    pathname: "/acme/issues",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path) => path,
  };
}

function renderButtons() {
  return renderWithI18n(
    <NavigationProvider value={nav()}>
      <TabHistoryButtons />
    </NavigationProvider>,
  );
}

function activeUrl(): string | undefined {
  const state = useTabStore.getState();
  const group = state.activeWorkspaceSlug
    ? state.byWorkspace[state.activeWorkspaceSlug]
    : undefined;
  return group?.tabs.find((tab) => tab.id === group.activeTabId)?.url;
}

beforeEach(() => {
  useTabStore.setState({ activeWorkspaceSlug: null, byWorkspace: {} });
  localStorage.clear();
  adapter.back.mockClear();
  adapter.forward.mockClear();
  adapter.canGoBack.mockClear();
  useTabStore.getState().switchWorkspace("acme");
});

describe("TabHistoryButtons", () => {
  it("is disabled until the tab has somewhere to go", () => {
    renderButtons();

    expect(screen.getByRole("button", { name: "Go back" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Go forward" })).toBeDisabled();
  });

  it("steps the active tab's own history in both directions", () => {
    act(() => {
      useTabStore.getState().navigateActiveSession("/acme/projects");
    });
    renderButtons();

    act(() => {
      fireEvent.click(screen.getByRole("button", { name: "Go back" }));
    });
    expect(activeUrl()).toBe("/acme/issues");
    expect(screen.getByRole("button", { name: "Go forward" })).toBeEnabled();

    act(() => {
      fireEvent.click(screen.getByRole("button", { name: "Go forward" }));
    });
    expect(activeUrl()).toBe("/acme/projects");
  });

  it("lists the tab's history on right-click and jumps to an entry", () => {
    act(() => {
      useTabStore.getState().navigateActiveSession("/acme/projects");
      useTabStore.getState().navigateActiveSession("/acme/inbox");
    });
    renderButtons();

    fireEvent.contextMenu(screen.getByRole("button", { name: "Go back" }), {
      clientX: 20,
      clientY: 20,
    });

    const items = screen.getAllByRole("menuitem");
    expect(items).toHaveLength(3);
    expect(items.at(-1)).toHaveAttribute("aria-current", "true");

    act(() => {
      fireEvent.click(items[0]!);
    });
    expect(activeUrl()).toBe("/acme/issues");
  });
});
