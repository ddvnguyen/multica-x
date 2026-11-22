import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { chatSessionsOptions, pendingChatTasksOptions } from "@multica/core/chat/queries";
import { useTabStore } from "@multica/core/tabs";
import type { ChatSession, PendingChatTasksResponse } from "@multica/core/types";
import { NavigationProvider, type NavigationAdapter } from "../navigation";
import { renderWithI18n } from "../test/i18n";
import { ChatBar } from "../chat/components/chat-bar";
import {
  ChatTabLeading,
  ChatTabsInHeaderProvider,
  HeaderChatControls,
  openChatSessionTab,
  useChatTabState,
} from "./chat-tabs";

const WS = "ws-test";

const h = vi.hoisted(() => ({
  chat: {
    isOpen: false,
    activeSessionId: null as string | null,
    selectedAgentId: "agent-1" as string | null,
    chatBarKept: [] as string[],
    chatBarDismissed: {} as Record<string, string>,
    setOpen: vi.fn(),
    setActiveSession: vi.fn(),
    setSelectedAgentId: vi.fn(),
    startNewChat: vi.fn(),
    keepChatInBar: vi.fn(),
    dismissChatFromBar: vi.fn(),
  },
}));

vi.mock("@multica/core/chat", () => ({
  useChatStore: Object.assign(
    (selector: (state: typeof h.chat) => unknown) => selector(h.chat),
    { getState: () => h.chat },
  ),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-test" }));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ chat: () => "/acme/chat" }),
  useCurrentWorkspace: () => ({ id: WS, slug: "acme" }),
  workspaceSlugForPath: (path: string) => path.split("/").filter(Boolean)[0] ?? null,
}));
vi.mock("@multica/core/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/api")>()),
  api: { getBaseUrl: () => "http://localhost" },
}));

const adapter: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/acme/issues",
  searchParams: new URLSearchParams(),
  hash: "",
  getShareableUrl: (path) => path,
};

function session(id: string, minute: number, overrides: Partial<ChatSession> = {}): ChatSession {
  const at = new Date(Date.UTC(2026, 10, 11, 8, minute)).toISOString();
  return {
    id,
    workspace_id: WS,
    agent_id: "agent-1",
    creator_id: "user-1",
    title: id,
    status: "active",
    has_unread: false,
    created_at: at,
    updated_at: at,
    ...overrides,
  };
}

const NO_PENDING_TASKS: PendingChatTasksResponse = { tasks: [] };

function renderIn(node: React.ReactNode, pending: PendingChatTasksResponse = NO_PENDING_TASKS) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  qc.setQueryData(
    chatSessionsOptions(WS).queryKey,
    [session("Read and done", 1), session("Weekly report", 3, { has_unread: true })],
  );
  qc.setQueryData(pendingChatTasksOptions(WS).queryKey, pending);
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <NavigationProvider value={adapter}>
        {node}
      </NavigationProvider>
    </QueryClientProvider>,
  );
}

/** The active workspace's tab group (the real store backs these tests). */
function group() {
  const state = useTabStore.getState();
  return state.byWorkspace[state.activeWorkspaceSlug ?? ""];
}

function chatTabs() {
  return (group()?.tabs ?? []).filter((tab) => tab.url.startsWith("/acme/chat"));
}

beforeEach(() => {
  h.chat.chatBarKept = [];
  h.chat.chatBarDismissed = {};
  h.chat.isOpen = false;
  h.chat.activeSessionId = null;
  vi.mocked(adapter.push).mockClear();
  h.chat.setOpen.mockClear();
  useTabStore.setState({
    activeWorkspaceSlug: null,
    byWorkspace: {},
    windowHistory: { entries: [], index: -1 },
  });
  useTabStore.getState().switchWorkspace("acme");
});

describe("chat tabs (M-413 round 5)", () => {
  // One session is one tab: the Chat list, a history row and a deep link all go
  // through the store's dedupe, so a conversation cannot gain a second tab.
  it("opens one tab per conversation and focuses it on the next open", () => {
    const first = openChatSessionTab("/acme/chat", { id: "Read and done", title: "Read and done" });
    expect(chatTabs()).toHaveLength(1);
    expect(chatTabs()[0]?.url).toBe("/acme/chat?session=Read%20and%20done");
    expect(group()?.activeTabId).toBe(first);

    const second = openChatSessionTab("/acme/chat", { id: "Read and done", title: "Read and done" });
    expect(second).toBe(first);
    expect(chatTabs()).toHaveLength(1);
  });

  // The strip's history used to add a chip beside the page; now it opens the
  // conversation's tab and leaves the page the reader was on alone.
  it("opens a conversation's tab from the strip's history without retargeting the page", () => {
    const pageTabId = group()?.activeTabId;
    renderIn(
      <ChatTabsInHeaderProvider>
        <HeaderChatControls />
      </ChatTabsInHeaderProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Chat history" }));
    fireEvent.click(screen.getByRole("button", { name: /Read and done/ }));

    expect(group()?.tabs.map((tab) => tab.url)).toEqual([
      "/acme/issues",
      "/acme/chat?session=Read%20and%20done",
    ]);
    expect(group()?.tabs.find((tab) => tab.id === pageTabId)?.url).toBe("/acme/issues");
    expect(adapter.push).not.toHaveBeenCalled();
  });

  it("focuses the tab the Chat list already opened for the same session", () => {
    const chatTabId = openChatSessionTab("/acme/chat", {
      id: "Read and done",
      title: "Read and done",
    });
    // Opening from the list activates the conversation; go back to the page so
    // the history click has something to focus.
    useTabStore.getState().setActiveTab(group()?.tabs[0]?.id ?? "");

    renderIn(
      <ChatTabsInHeaderProvider>
        <HeaderChatControls />
      </ChatTabsInHeaderProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Chat history" }));
    fireEvent.click(screen.getByRole("button", { name: /Read and done/ }));

    expect(chatTabs()).toHaveLength(1);
    expect(group()?.activeTabId).toBe(chatTabId);
  });

  it("closes the floating window when a conversation becomes the page", () => {
    h.chat.isOpen = true;
    renderIn(
      <ChatTabsInHeaderProvider>
        <HeaderChatControls />
      </ChatTabsInHeaderProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Chat history" }));
    fireEvent.click(screen.getByRole("button", { name: /Read and done/ }));

    expect(h.chat.setOpen).toHaveBeenCalledWith(false);
  });

  // M-413 round 2: the footer is gone in this shell, so starting a chat and
  // finding an old one have to be reachable from the strip.
  it("starts a new chat from the strip's control", () => {
    renderIn(
      <ChatTabsInHeaderProvider>
        <HeaderChatControls />
      </ChatTabsInHeaderProvider>,
    );

    const start = screen.getByRole("button", { name: "New chat" });
    fireEvent.click(start);
    expect(h.chat.startNewChat).toHaveBeenCalledTimes(1);
    // The floating window anchors to the control that started it.
    expect(start).toHaveAttribute("data-chat-anchor", "new");
  });

  it("renders no chat controls in a shell without the header placement", () => {
    renderIn(<HeaderChatControls />);
    expect(screen.queryByRole("button", { name: "New chat" })).toBeNull();
  });

  it("takes the sessions out of the bottom bar it merged into", () => {
    renderIn(
      <ChatTabsInHeaderProvider>
        <ChatBar />
      </ChatTabsInHeaderProvider>,
    );

    const bar = document.querySelector('[data-slot="chat-bar"]');
    expect(bar?.textContent ?? "").not.toContain("Weekly report");
    // Ask Multica and the history stay: they start and find chats.
    expect(within(bar as HTMLElement).getByRole("button", { name: /bar\.ask|Ask/i })).toBeTruthy();
  });
});

describe("ChatTabLeading (M-413 round 5)", () => {
  function Probe({ url }: { url: string }) {
    const state = useChatTabState(url);
    return (
      <ChatTabLeading state={state}>
        <span data-testid="tab-visual" />
      </ChatTabLeading>
    );
  }

  it("shows the unread dot on a conversation's tab", () => {
    const { container } = renderIn(<Probe url="/acme/chat?session=Weekly%20report" />);
    expect(container.querySelector("[data-chat-tab-unread]")).not.toBeNull();
    expect(container.querySelector("[data-chat-tab-running]")).toBeNull();
    expect(screen.getByTestId("tab-visual")).toBeTruthy();
  });

  it("replaces the icon with a spinner while a task runs", () => {
    const { container } = renderIn(<Probe url="/acme/chat?session=Weekly%20report" />, {
      tasks: [{ chat_session_id: "Weekly report" } as PendingChatTasksResponse["tasks"][number]],
    });
    expect(container.querySelector("[data-chat-tab-running]")).not.toBeNull();
    // The spinner takes the leading slot's place; the icon does not also show.
    expect(screen.queryByTestId("tab-visual")).toBeNull();
  });

  it("leaves a page tab's own visual alone", () => {
    const { container } = renderIn(<Probe url="/acme/issues" />);
    expect(container.querySelector("[data-chat-tab-running]")).toBeNull();
    expect(container.querySelector("[data-chat-tab-unread]")).toBeNull();
    expect(screen.getByTestId("tab-visual")).toBeTruthy();
  });
});
