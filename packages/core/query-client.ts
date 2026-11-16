import { QueryClient } from "@tanstack/react-query";
import { setWorkspaceSlugLookup } from "./paths/workspace-slugs";
import { workspaceKeys } from "./workspace/queries";
import type { Workspace } from "./types";

function httpStatusOf(error: unknown): number | null {
  if (typeof error !== "object" || error === null) return null;
  if (!("status" in error)) return null;
  const status = error.status;
  return typeof status === "number" ? status : null;
}

/**
 * Whether a failed query deserves another attempt (M-373).
 *
 * A 4xx is the server's answer, not a transport failure: the credential is
 * dead (401), the caller may not do that (403/404), or the input is wrong
 * (400/422). Replaying it doubles the failed-request count behind the red
 * chip without ever changing the answer — 401 already ends the session
 * through the ApiClient's `onUnauthorized` hook, and every other 4xx needs
 * the user (or the code) to change something first. Only 5xx and transport
 * failures retry, once.
 */
export function shouldRetryQuery(failureCount: number, error: unknown): boolean {
  const status = httpStatusOf(error);
  if (status !== null && status >= 400 && status < 500) return false;
  return failureCount < 1;
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: Infinity,
        gcTime: 10 * 60 * 1000, // 10 minutes
        refetchOnWindowFocus: false,
        refetchOnReconnect: true,
        retry: shouldRetryQuery,
      },
      mutations: {
        retry: false,
      },
    },
  });
}

/**
 * Point path resolution at `client`'s workspace list. Reading the cache on
 * every lookup covers fetches, hydration, mutations and logout/clear. Owned by
 * the mounted provider rather than the constructor: StrictMode builds and
 * discards an extra client, which must never become the bound one.
 */
export function bindWorkspaceSlugLookup(client: QueryClient): () => void {
  return setWorkspaceSlugLookup(
    (slug) =>
      client
        .getQueryData<Workspace[]>(workspaceKeys.list())
        ?.some((workspace) => workspace.slug === slug) ?? false,
  );
}
