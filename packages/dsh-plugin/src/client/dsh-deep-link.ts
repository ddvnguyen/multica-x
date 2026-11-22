/**
 * `?dsh_session=<id>` deep links from a Multica run row (M-132 E1).
 *
 * The Multica server stamps `dsh_session_url` with the run's own session, so
 * "Open session" addresses one session instead of the app. dsh's web client
 * cannot act on that address by itself: at `a6f2f66c` nothing under
 * `packages/client` or `apps/web/src` reads `location.search`, the hash or a
 * route, and the shell restores its last selection from browser storage. This
 * plugin is loaded into that page, so it is the seam that turns the parameter
 * into one navigation — the same `uiWorkspace.openSession` a sidebar row calls,
 * which reveals the Conversation and selects that Session.
 *
 * Every step is gated: no parameter, no `uiWorkspace` face (a host built
 * without ui-workspace), or a session this shell never lists leaves the page
 * exactly as it was. The parameter is consumed once, so a later reload does not
 * re-assert a navigation the reader has already left.
 *
 * The Trajectory View is not addressable this way: dsh selects it per Session
 * from the header and persists that choice in the conversation store, and the
 * View is hidden unless developer tools are enabled
 * (`packages/client/ui-conversation/src/client/apply.ts:210-224`). Landing on
 * the run's own Session is what this link can promise; the View stays the
 * reader's last choice for that Session.
 */

/** Query parameter the server appends to `dsh_session_url`. */
export const DSH_SESSION_PARAM = "dsh_session";

/** The shell's session list, declared as the members this module reads. */
export interface DeepLinkSessions {
  readonly list: {
    getSnapshot(): {
      readonly byId: Record<string, unknown>;
      /** `pending` until the first successful list pull, then `ready`. */
      readonly phase?: string;
    };
    subscribe(listener: () => void): () => void;
  };
}

/** The shell navigation this module calls (`uiWorkspace.openSession`). */
export interface DeepLinkNavigation {
  openSession(sessionId: string): void;
}

export interface DshDeepLinkOptions {
  /** `location.search` as the page loaded. */
  readonly search: string;
  /** Shell session list; null when the host does not provide one. */
  readonly sessions: DeepLinkSessions | null;
  /** Shell session navigation; null when the host does not provide it. */
  readonly navigation: DeepLinkNavigation | null;
  /**
   * Rewrite the document URL with the parameter removed. Called as soon as the
   * value is read, so the link is consumed even when this host cannot act on
   * it. Null keeps the URL untouched.
   */
  readonly replaceSearch?: ((search: string) => void) | null;
}

/** The parameter value, or null when absent or blank. */
export function readDshSessionParam(search: string): string | null {
  const value = new URLSearchParams(search).get(DSH_SESSION_PARAM)?.trim();
  return value === undefined || value === "" ? null : value;
}

/** `search` without the parameter, keeping every other query entry. */
export function withoutDshSessionParam(search: string): string {
  const params = new URLSearchParams(search);
  params.delete(DSH_SESSION_PARAM);
  const rest = params.toString();
  return rest === "" ? "" : `?${rest}`;
}

/**
 * Consume a boot-time `?dsh_session=` and select that Session.
 *
 * The list is pulled before the shell reports `ready`, so a target that is not
 * there yet is a normal early state, not a failure: this subscribes and selects
 * it on the snapshot that carries it. A `ready` list without it is a session
 * this shell cannot open, and the subscription ends instead of waiting forever.
 * @param options - the boot search, the two shell faces, and the URL rewrite.
 * @returns a disposer for a subscription still waiting on the session list, or
 *   null when the link was consumed without one.
 */
export function followDshSessionDeepLink(options: DshDeepLinkOptions): (() => void) | null {
  const sessionId = readDshSessionParam(options.search);
  if (sessionId === null) return null;
  // Consumed once, before any await: the reader's own next selection must not
  // be undone by following the same URL again.
  options.replaceSearch?.(withoutDshSessionParam(options.search));
  const { sessions, navigation } = options;
  if (sessions === null || navigation === null) return null;
  if (hasSession(sessions, sessionId)) {
    navigation.openSession(sessionId);
    return null;
  }
  let done = false;
  // Assigned after `subscribe`, which may call back synchronously on a host
  // that publishes a snapshot at subscription time.
  let unsubscribe = (): void => {};
  const stop = (): void => {
    if (done) return;
    done = true;
    unsubscribe();
  };
  unsubscribe = sessions.list.subscribe(() => {
    if (hasSession(sessions, sessionId)) {
      stop();
      navigation.openSession(sessionId);
      return;
    }
    if (sessions.list.getSnapshot().phase === "ready") stop();
  });
  return stop;
}

function hasSession(sessions: DeepLinkSessions, sessionId: string): boolean {
  return Object.hasOwn(sessions.list.getSnapshot().byId, sessionId);
}
