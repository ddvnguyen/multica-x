import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { chatSessionsOptions } from "@multica/core/chat/queries";
import { countUnreadChatMessages } from "@multica/core/chat/unread";
import { useInboxUnreadCount } from "@multica/core/inbox/queries";

type BadgeCapableAPI = {
  setUnreadBadge?: (count: number) => void;
};

function getDesktopAPI(): BadgeCapableAPI | undefined {
  if (typeof window === "undefined") return undefined;
  return (window as unknown as { desktopAPI?: BadgeCapableAPI }).desktopAPI;
}

/**
 * Tell the muximux portal how many unread items there are. The portal embeds
 * this app in a same-origin iframe at /p/<port>/ and mirrors the number onto
 * the installed PWA's badge, which outlives the tab. Standalone tabs (and the
 * desktop shell) have no parent, so they skip it; the portal drops anything
 * whose source is not its active app frame.
 */
function postPortalBadge(count: number): void {
  if (typeof window === "undefined" || window.parent === window) return;
  try {
    window.parent.postMessage({ type: "portal:badge", count }, location.origin);
  } catch {
    /* cross-origin parent or a blocked postMessage */
  }
}

/**
 * Mirror Inbox + Chat unread counts onto the OS dock/taskbar badge and the
 * hosting portal's app badge. No-op on a plain web tab (no `desktopAPI`, no
 * parent frame) and on the login screen (no workspace ⇒ count defaults to 0,
 * which clears any stale badge from a previous session). The badge is on
 * screen while the window is minimized, so it keeps the summary live then.
 */
export function useDesktopUnreadBadge(wsId: string | null | undefined): void {
  const inboxCount = useInboxUnreadCount(wsId, { whileHidden: true });
  const { data: sessions } = useQuery({
    ...chatSessionsOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const count = inboxCount + (wsId ? countUnreadChatMessages(sessions) : 0);
  useEffect(() => {
    getDesktopAPI()?.setUnreadBadge?.(count);
    postPortalBadge(count);
  }, [count]);
}
