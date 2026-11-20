import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { chatSessionsOptions, pendingChatTasksOptions } from "@multica/core/chat/queries";
import type { ChatSession } from "@multica/core/types";
import { NavigationProvider, type NavigationAdapter } from "../navigation";
import { renderWithI18n } from "../test/i18n";
import { ChatBar } from "../chat/components/chat-bar";
import {
  ChatTabsInHeaderProvider,
  HeaderChatControls,
  HeaderChatTabs,
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

function renderIn(node: React.ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  qc.setQueryData(
    chatSessionsOptions(WS).queryKey,
    [session("Read and done", 1), session("Weekly report", 3, { has_unread: true })],
  );
  qc.setQueryData(pendingChatTasksOptions(WS).queryKey, { tasks: [] });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <NavigationProvider value={adapter}>
        {node}
      </NavigationProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  h.chat.chatBarKept = [];
  h.chat.chatBarDismissed = {};
  h.chat.isOpen = false;
  h.chat.activeSessionId = null;
  vi.mocked(adapter.push).mockClear();
});

describe("header chat tabs (M-413)", () => {
  it("renders one tab per open chat, and a click opens that conversation", () => {
    renderIn(
      <ChatTabsInHeaderProvider>
        <HeaderChatTabs />
      </ChatTabsInHeaderProvider>,
    );

    const tab = document.querySelector<HTMLElement>('[data-chat-tab="Weekly report"]');
    expect(tab).not.toBeNull();
    // The unread state rides in the accessible name, not a second badge.
    const open = tab?.querySelector("button");
    expect(open?.getAttribute("aria-label")).toMatch(/^Weekly report, .+/);

    fireEvent.click(open!);
    expect(adapter.push).toHaveBeenCalledWith("/acme/chat?session=Weekly%20report");
    expect(h.chat.setActiveSession).toHaveBeenCalledWith("Weekly report");
  });

  it("closes a chat tab with its hover control", () => {
    renderIn(
      <ChatTabsInHeaderProvider>
        <HeaderChatTabs />
      </ChatTabsInHeaderProvider>,
    );

    const tab = document.querySelector<HTMLElement>('[data-chat-tab="Weekly report"]');
    const close = tab?.querySelectorAll("button")[1];
    fireEvent.click(close!);
    expect(h.chat.dismissChatFromBar).toHaveBeenCalled();
  });

  it("renders nothing in a shell that keeps its chats in the bottom bar", () => {
    renderIn(<HeaderChatTabs />);
    expect(screen.queryByRole("button", { name: /Weekly report/ })).toBeNull();
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

  it("opens an old chat as the page from the strip's history", async () => {
    renderIn(
      <ChatTabsInHeaderProvider>
        <HeaderChatControls />
      </ChatTabsInHeaderProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Chat history" }));
    fireEvent.click(await screen.findByRole("button", { name: /Read and done/ }));

    expect(adapter.push).toHaveBeenCalledWith("/acme/chat?session=Read%20and%20done");
    expect(h.chat.setActiveSession).toHaveBeenCalledWith("Read and done");
    expect(h.chat.setOpen).not.toHaveBeenCalledWith(true);
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
