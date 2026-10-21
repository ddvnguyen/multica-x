import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { FreebuffAccount } from "../types/agent";
import { runtimeKeys } from "./queries";

// Freebuff account state for a runtime.
//
// The account lives on the daemon's machine, so what the UI shows is always a
// snapshot the daemon pushed. While a browser login is in flight the
// snapshot changes on its own — the daemon reports the login URL, then the
// outcome — so the query polls quickly until the login settles and slowly
// afterwards.

export const freebuffKeys = {
  account: (runtimeId: string) => ["runtimes", "freebuff", runtimeId] as const,
};

// The connect states that mean "something is still moving".
const PENDING_CONNECT_STATES = new Set(["requested", "awaiting_login"]);

export function freebuffAccountOptions(runtimeId: string) {
  return queryOptions({
    queryKey: freebuffKeys.account(runtimeId),
    queryFn: () => api.getFreebuffAccount(runtimeId),
    // Fast while a login is in flight (the URL and the outcome arrive as
    // separate pushes), slow otherwise — the session slot counts down on its
    // own and the daemon refreshes it every minute anyway.
    refetchInterval: (query) => {
      const state = query.state.data?.connect?.state;
      return state && PENDING_CONNECT_STATES.has(state) ? 3000 : 30000;
    },
    staleTime: 0,
  });
}

export function useRequestFreebuffConnect(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (runtimeId: string) => api.requestFreebuffConnect(runtimeId),
    onSettled: (_data, _error, runtimeId) => {
      qc.invalidateQueries({ queryKey: freebuffKeys.account(runtimeId) });
      // The runtime row itself does not change, but the request is recorded
      // on it, so refresh the list too.
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
    },
  });
}

export type { FreebuffAccount };
