/** @vitest-environment jsdom */
import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { WSClient } from "../api/ws-client";
import { issueKeys } from "../issues/queries";
import { createQueryClient } from "../query-client";
import { useRealtimeSync, type RealtimeSyncStores } from "./use-realtime-sync";

vi.mock("../platform/workspace-storage", () => ({
  getCurrentWsId: () => "ws-1",
  getCurrentSlug: () => "test-ws",
  createWorkspaceAwareStorage: (adapter: unknown) => adapter,
  registerForWorkspaceRehydration: () => {},
}));
vi.mock("../paths", () => ({
  useHasOnboarded: () => true,
  resolvePostAuthDestination: () => "/",
}));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

/**
 * The ask/plan panel (M-120) reads one cache per issue, and every state
 * transition it renders is a task lifecycle event: `task:blocked` is a card or
 * plan appearing, `task:queued` the answer or verdict closing it,
 * `task:cancelled` the server withdrawing it. The `task:` prefix refresh is
 * what makes an already-open issue page follow those transitions — without it
 * the panel only catches up on the next refetch.
 */
it("marks the issue's ask/plan read stale on a task lifecycle event", async () => {
  setApiInstance({} as unknown as ApiClient);
  const qc = createQueryClient();
  const invalidate = vi.spyOn(qc, "invalidateQueries");

  const anyHandlers: Array<(msg: { type: string }) => void> = [];
  const ws = {
    on: () => () => {},
    onAny: (handler: (msg: { type: string }) => void) => {
      anyHandlers.push(handler);
      return () => {};
    },
    onReconnect: () => () => {},
  } as unknown as WSClient;
  const stores = {
    authStore: { getState: () => ({ user: { id: "u1" } }) },
  } as unknown as RealtimeSyncStores;

  function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  }

  const shell = renderHook(() => useRealtimeSync(ws, stores), { wrapper: Wrapper });

  await act(async () => {
    anyHandlers.forEach((handler) => handler({ type: "task:blocked" }));
  });

  await waitFor(() =>
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: issueKeys.askPlanAll("ws-1"),
    }),
  );
  shell.unmount();
});
