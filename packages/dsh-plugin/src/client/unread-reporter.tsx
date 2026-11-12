import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { inboxUnreadSummaryEnabled, inboxUnreadSummaryOptions } from "../fork";
import { setUnread, totalUnreadCount } from "./unread-store";

/**
 * Push the island's unread summary into the rail dot's store. Renders nothing.
 *
 * The dot has to clear the moment the inbox is read, not at the next host
 * poll, so an observer of the summary writes the store whenever the data
 * changes — the core realtime updaters already invalidate this key on read.
 * It queries `inboxUnreadSummaryOptions()`, the same key * `InboxPage`'s `useInboxUnreadCount` reads, so both observers share one
 * cache entry and this reporter adds zero requests of its own.
 */
export function UnreadReporter({ workspaceId }: { workspaceId: string }): null {
  const { data } = useQuery({
    ...inboxUnreadSummaryOptions(),
    enabled: inboxUnreadSummaryEnabled(workspaceId),
  });

  useEffect(() => {
    // Gated off, or nothing has resolved yet: keep the store as it is rather
    // than reading "no data" as "nothing unread".
    if (data === undefined) return;
    setUnread(totalUnreadCount(data));
  }, [data]);

  return null;
}
