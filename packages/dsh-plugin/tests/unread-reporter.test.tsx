import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api";
import type { InboxWorkspaceUnread } from "@multica/core/types/inbox";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";
import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { UnreadReporter } from "../src/client/unread-reporter";
import { getUnread, setUnread } from "../src/client/unread-store";

/**
 * The island boots this singleton through CoreProvider; the reporter test sets
 * it directly so the real `inboxUnreadSummaryOptions()` query resolves without
 * a server behind it.
 */
function stubSummary(rows: InboxWorkspaceUnread[]) {
  const getInboxUnreadSummary = vi.fn(async () => rows);
  setApiInstance({ getInboxUnreadSummary } as unknown as ApiClient);
  return getInboxUnreadSummary;
}

function renderReporter(workspaceId: string, client: QueryClient) {
  return render(
    <QueryClientProvider client={client}>
      <UnreadReporter workspaceId={workspaceId} />
    </QueryClientProvider>,
  );
}

describe("UnreadReporter", () => {
  beforeEach(() => {
    setUnread(0);
  });

  afterEach(() => {
    setUnread(0);
    vi.clearAllMocks();
  });

  it("writes the summed summary into the store when the query resolves", async () => {
    const getInboxUnreadSummary = stubSummary([
      { workspace_id: "ws-1", count: 2 },
      { workspace_id: "ws-2", count: 3 },
    ]);

    renderReporter("ws-1", new QueryClient());

    await waitFor(() => expect(getUnread()).toBe(5));
    expect(getInboxUnreadSummary).toHaveBeenCalledTimes(1);
  });

  it("writes nothing and fetches nothing while gated off", async () => {
    setUnread(7);
    const getInboxUnreadSummary = stubSummary([
      { workspace_id: "ws-1", count: 1 },
    ]);

    renderReporter("", new QueryClient());
    // Mount effects have run; let any (incorrectly scheduled) fetch reach the
    // api stub before asserting it never happened.
    await act(async () => {
      await Promise.resolve();
    });

    expect(getInboxUnreadSummary).not.toHaveBeenCalled();
    expect(getUnread()).toBe(7);
  });
});
