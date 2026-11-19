import type { ReactNode } from "react";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider, focusManager } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { chatKeys } from "@multica/core/chat/queries";
import type { ChatSession } from "@multica/core/types";
import { useDesktopUnreadBadge } from "./use-desktop-unread-badge";

afterEach(() => {
  cleanup(); focusManager.setFocused(undefined);
  delete (window as unknown as Record<string, unknown>).desktopAPI;
  setApiInstance(undefined as unknown as ApiClient);
});

it("adds Chat messages to Inbox unread while backgrounded and clears read/logout counts", async () => {
  let chatUnread = 0;
  const listChatSessions = vi.fn(async () => [{ id: "chat-a", unread_count: chatUnread }] as ChatSession[]);
  setApiInstance({
    getInboxUnreadSummary: async () => [{ workspace_id: "ws-a", count: 2 }],
    listChatSessions,
  } as unknown as ApiClient);
  const setUnreadBadge = vi.fn();
  Object.defineProperty(window, "desktopAPI", { configurable: true, value: { setUnreadBadge } });
  focusManager.setFocused(false);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  const view = renderHook(({ wsId }: { wsId: string | null }) => useDesktopUnreadBadge(wsId), {
    wrapper, initialProps: { wsId: "ws-a" as string | null },
  });
  await waitFor(() => expect(setUnreadBadge).toHaveBeenLastCalledWith(2));
  chatUnread = 3;
  await act(async () => { await qc.invalidateQueries({ queryKey: chatKeys.sessions("ws-a") }); });
  await waitFor(() => expect(setUnreadBadge).toHaveBeenLastCalledWith(5));
  chatUnread = 0;
  await act(async () => { await qc.invalidateQueries({ queryKey: chatKeys.sessions("ws-a") }); });
  await waitFor(() => expect(setUnreadBadge).toHaveBeenLastCalledWith(2));
  view.rerender({ wsId: null });
  await waitFor(() => expect(setUnreadBadge).toHaveBeenLastCalledWith(0));
  qc.clear();
});

it("relays the unread count to the hosting portal", async () => {
  setApiInstance({
    getInboxUnreadSummary: async () => [{ workspace_id: "ws-a", count: 4 }],
    listChatSessions: async () => [] as ChatSession[],
  } as unknown as ApiClient);
  const postMessage = vi.fn();
  const original = Object.getOwnPropertyDescriptor(window, "parent");
  Object.defineProperty(window, "parent", { configurable: true, value: { postMessage } });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  try {
    const view = renderHook(() => useDesktopUnreadBadge("ws-a"), { wrapper });
    await waitFor(() => expect(postMessage).toHaveBeenLastCalledWith(
      { type: "portal:badge", count: 4 }, window.location.origin));
    view.rerender();
    // The first paint reports 0 (clearing any stale badge) before the queries
    // resolve; nothing other than the live count may reach the parent.
    const counts = postMessage.mock.calls.map(([message]) => (message as { count: number }).count);
    expect(counts[counts.length - 1]).toBe(4);
    expect(counts.every((count) => count === 0 || count === 4)).toBe(true);
    for (const [, origin] of postMessage.mock.calls) {
      expect(origin).toBe(window.location.origin);
    }
  } finally {
    if (original) Object.defineProperty(window, "parent", original);
    else delete (window as unknown as Record<string, unknown>).parent;
    qc.clear();
  }
});

it("stays silent when it is not embedded in a portal", async () => {
  setApiInstance({
    getInboxUnreadSummary: async () => [{ workspace_id: "ws-a", count: 4 }],
    listChatSessions: async () => [] as ChatSession[],
  } as unknown as ApiClient);
  const postMessage = vi.spyOn(window, "postMessage").mockImplementation(() => {});
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  try {
    const view = renderHook(() => useDesktopUnreadBadge("ws-a"), { wrapper });
    await waitFor(() => expect(qc.getQueryData(chatKeys.sessions("ws-a"))).toBeDefined());
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(postMessage).not.toHaveBeenCalled();
    view.unmount();
  } finally {
    postMessage.mockRestore();
    qc.clear();
  }
});
