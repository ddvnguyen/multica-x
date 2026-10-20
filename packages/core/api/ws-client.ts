import type { WSMessage, WSEventType } from "../types/events";
import { type Logger, noopLogger } from "../logger";

type EventHandler = (payload: unknown, actorId?: string, actorType?: string) => void;

// Cap how much of an unparseable frame we put into the log. A malformed or
// rogue server can stream arbitrarily large garbage, and the warn handler may
// be a console / IPC bridge whose buffers we don't want to blow.
const UNPARSEABLE_LOG_MAX_CHARS = 200;

// Reconnect backoff parameters. A flat delay causes a thundering herd when many
// clients reconnect after a server restart; exponential backoff with jitter
// spreads the reconnection attempts over time. The client retries indefinitely
// (capped at RECONNECT_MAX_DELAY_MS) because the web/desktop UI does not yet
// expose a visible disconnected state or manual retry action.
const RECONNECT_BASE_DELAY_MS = 1_000;
const RECONNECT_MAX_DELAY_MS = 30_000;

// Application-level heartbeat. The server keeps a WebSocket open for
// `pongWait` (60s) and refreshes that deadline on any inbound frame, so a
// client that stays silent is disconnected on a timer. A protocol-level
// PING/PONG would also do it, but those are control frames: a CDN edge that
// terminates the client WebSocket and re-originates it (Cloudflare does)
// does not always carry them through to the origin, and the connection then
// dies on the server's deadline while both ends believe it is fine. The
// client answers that by reconnecting and refetching the whole workspace,
// which is what makes the web UI feel like it stopped responding.
//
// A heartbeat is a data frame, so it survives every hop in the chain. 20s
// keeps it comfortably inside the 60s server deadline with room for a lost
// frame or two. The mobile client uses the same exchange (see
// apps/mobile/data/realtime/ws-client.ts) — a text `{"type":"ping"}` answered
// by `{"type":"pong"}`.
const HEARTBEAT_INTERVAL_MS = 20_000;
// A missing pong means the socket is half-open: the peer is gone but the
// browser has not noticed yet. Treating that as a dead connection is what
// turns a silently stale UI into a prompt reconnect.
//
// The window has to be longer than the slowest round trip the deployment can
// produce. The UI is usually reached through a CDN edge that re-originates
// the WebSocket, and that hop has been measured at 15-20s on a bad route —
// closing the socket after 10s would turn a slow-but-alive connection into a
// reconnect loop, which is the failure this heartbeat exists to prevent. 30s
// still sits well inside the server's 60s liveness window, so the server
// never drops a connection the client still considers alive.
const HEARTBEAT_TIMEOUT_MS = 30_000;

function summarizeUnparseable(data: unknown): string {
  const text = typeof data === "string" ? data : String(data);
  if (text.length <= UNPARSEABLE_LOG_MAX_CHARS) return text;
  return `${text.slice(0, UNPARSEABLE_LOG_MAX_CHARS)}… (truncated, ${text.length} chars total)`;
}

/** Identifies the WS client to the server. Sent as `client_platform`,
 *  `client_version`, and `client_os` query parameters on the upgrade URL —
 *  browsers cannot set custom headers on WebSocket handshakes, so query
 *  params are the only portable channel. */
export interface WSClientIdentity {
  platform?: string;
  version?: string;
  os?: string;
}

export class WSClient {
  private ws: WebSocket | null = null;
  private baseUrl: string;
  private token: string | null = null;
  private workspaceSlug: string | null = null;
  private cookieAuth = false;
  private identity: WSClientIdentity | undefined;
  private handlers = new Map<WSEventType, Set<EventHandler>>();
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private heartbeatTimer: ReturnType<typeof setInterval> | null = null;
  private pongTimer: ReturnType<typeof setTimeout> | null = null;
  private awaitingPong = false;
  private reconnectAttempt = 0;
  private hasConnectedBefore = false;
  // One-shot per connection. A non-conforming frame can repeat hundreds of
  // times per session, so we log the first drop and suppress the rest. Reset
  // on each connect() so a fresh connection logs once again.
  private badFrameLogged = false;
  private onReconnectCallbacks = new Set<() => void>();
  private anyHandlers = new Set<(msg: WSMessage) => void>();
  private logger: Logger;

  /**
   * Reads the current token at connect time. A reconnect can happen long
   * after this client was built — long enough for a sliding session to have
   * been renewed in between — so the token the auth frame carries has to be
   * looked up now, not captured once (MUL-7436).
   */
  private getToken: (() => string | null) | undefined;

  constructor(
    url: string,
    options?: {
      logger?: Logger;
      cookieAuth?: boolean;
      identity?: WSClientIdentity;
      getToken?: () => string | null;
    },
  ) {
    this.baseUrl = url;
    this.logger = options?.logger ?? noopLogger;
    this.cookieAuth = options?.cookieAuth ?? false;
    this.identity = options?.identity;
    this.getToken = options?.getToken;
  }

  setAuth(token: string | null, workspaceSlug: string) {
    this.token = token;
    this.workspaceSlug = workspaceSlug;
  }

  connect() {
    this.badFrameLogged = false;
    const url = new URL(this.baseUrl);
    // Token is never sent as a URL query parameter — it would be logged by
    // proxies, CDNs, and browser history.  In cookie mode the HttpOnly cookie
    // is sent automatically with the upgrade request.  In token mode the token
    // is delivered as the first WebSocket message after the connection opens.
    if (this.workspaceSlug)
      url.searchParams.set("workspace_slug", this.workspaceSlug);
    if (this.identity?.platform)
      url.searchParams.set("client_platform", this.identity.platform);
    if (this.identity?.version)
      url.searchParams.set("client_version", this.identity.version);
    if (this.identity?.os)
      url.searchParams.set("client_os", this.identity.os);

    this.ws = new WebSocket(url.toString());

    this.ws.onopen = () => {
      this.startHeartbeat();
      const token = this.getToken?.() ?? this.token;
      if (!this.cookieAuth && token) {
        this.ws!.send(
          JSON.stringify({ type: "auth", payload: { token } }),
        );
        return;
      }

      this.onAuthenticated();
    };

    this.ws.onmessage = (event) => {
      let msg: WSMessage;
      try {
        msg = JSON.parse(event.data as string) as WSMessage;
      } catch {
        this.logger.warn(
          "ws: received unparseable message",
          summarizeUnparseable(event.data),
        );
        return;
      }
      // Trust boundary: a frame must be an object carrying a string `type`.
      // The server protocol guarantees this for every frame, but a
      // non-conforming frame — an out-of-protocol frame injected by a proxy /
      // browser extension, or a bare JSON primitive — must degrade to a no-op
      // here. Without this guard every downstream consumer (the onAny
      // dispatcher and every ws.on subscriber) runs against a bad shape;
      // `msg.type.split(...)` in the realtime sync threw an uncaught TypeError
      // out of onmessage and surfaced as a flood of global `$exception` events
      // (MUL-3418). Validate once at the boundary, trust the shape downstream.
      if (!msg || typeof (msg as { type?: unknown }).type !== "string") {
        if (!this.badFrameLogged) {
          this.badFrameLogged = true;
          this.logger.warn(
            "ws: dropping frame without a string type",
            summarizeUnparseable(event.data),
          );
        }
        return;
      }
      if ((msg as any).type === "auth_ack") {
        this.onAuthenticated();
        return;
      }
      // The heartbeat's own answer. Nothing subscribes to it, and letting it
      // fall through would dispatch it to every `onAny` listener for no
      // reason.
      if ((msg as any).type === "pong") {
        this.awaitingPong = false;
        this.clearPongTimer();
        return;
      }
      this.logger.debug("received", msg.type);
      const eventHandlers = this.handlers.get(msg.type);
      if (eventHandlers) {
        for (const handler of eventHandlers) {
          handler(msg.payload, msg.actor_id, msg.actor_type);
        }
      }
      for (const handler of this.anyHandlers) {
        handler(msg);
      }
    };

    this.ws.onclose = () => {
      this.stopHeartbeat();
      this.scheduleReconnect();
    };

    this.ws.onerror = () => {
      // Suppress — onclose handles reconnect; errors during StrictMode
      // double-fire are expected in dev and harmless.
    };
  }

  /**
   * Keeps the WebSocket alive with an application-level heartbeat. The
   * server treats any inbound frame as proof of life, so this survives
   * proxies and CDNs that drop protocol-level PING/PONG control frames.
   */
  private startHeartbeat() {
    this.stopHeartbeat();
    this.sendHeartbeat();
    this.heartbeatTimer = setInterval(
      () => this.sendHeartbeat(),
      HEARTBEAT_INTERVAL_MS,
    );
  }

  private sendHeartbeat() {
    if (this.ws?.readyState !== WebSocket.OPEN) return;
    // One heartbeat in flight at a time. On a slow link the pong can take
    // longer than the interval, and a second ping would only pile up.
    if (this.awaitingPong) return;
    this.awaitingPong = true;
    try {
      this.ws.send(JSON.stringify({ type: "ping" }));
    } catch {
      // A send failure means the socket is already broken. Closing it hands
      // the recovery to the normal onclose → reconnect path.
      this.ws.close();
      return;
    }
    this.clearPongTimer();
    this.pongTimer = setTimeout(() => {
      this.pongTimer = null;
      if (!this.awaitingPong) return;
      // The peer stopped answering: the connection is half-open. Close it so
      // onclose can reconnect instead of leaving the UI on a socket that
      // silently stops delivering events.
      this.logger.warn("ws: heartbeat timeout, closing stale connection");
      this.ws?.close();
    }, HEARTBEAT_TIMEOUT_MS);
  }

  private clearPongTimer() {
    if (this.pongTimer) {
      clearTimeout(this.pongTimer);
      this.pongTimer = null;
    }
  }

  private stopHeartbeat() {
    if (this.heartbeatTimer) {
      clearInterval(this.heartbeatTimer);
      this.heartbeatTimer = null;
    }
    this.clearPongTimer();
    this.awaitingPong = false;
  }

  /**
   * Schedule a reconnection attempt with exponential backoff and jitter.
   * Retries indefinitely with a capped delay because the web/desktop UI
   * does not yet expose a visible disconnected state or manual retry action.
   */
  private scheduleReconnect() {
    const base = Math.min(
      RECONNECT_BASE_DELAY_MS * 2 ** this.reconnectAttempt,
      RECONNECT_MAX_DELAY_MS,
    );
    // ±20 % jitter so clients that disconnected at the same time don't
    // reconnect in lockstep.
    const jitter = base * 0.2 * (Math.random() * 2 - 1);
    const delay = Math.round(
      Math.min(base + jitter, RECONNECT_MAX_DELAY_MS),
    );

    this.reconnectAttempt++;
    this.logger.warn(
      `ws: disconnected, reconnecting in ${delay}ms (attempt ${this.reconnectAttempt})`,
    );
    this.reconnectTimer = setTimeout(() => this.connect(), delay);
  }

  private onAuthenticated() {
    this.logger.info("connected");
    const recoveredConnection = this.hasConnectedBefore || this.reconnectAttempt > 0;
    this.reconnectAttempt = 0;
    if (recoveredConnection) {
      for (const cb of this.onReconnectCallbacks) {
        try {
          cb();
        } catch {
          // ignore reconnect callback errors
        }
      }
    }
    this.hasConnectedBefore = true;
  }

  disconnect() {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.stopHeartbeat();
    if (this.ws) {
      // Remove handlers before close to prevent onclose from scheduling a reconnect
      this.ws.onclose = null;
      this.ws.onerror = null;
      this.ws.close();
      this.ws = null;
    }
    this.hasConnectedBefore = false;
    this.reconnectAttempt = 0;
    this.handlers.clear();
    this.anyHandlers.clear();
    this.onReconnectCallbacks.clear();
  }

  on(event: WSEventType, handler: EventHandler) {
    if (!this.handlers.has(event)) {
      this.handlers.set(event, new Set());
    }
    this.handlers.get(event)!.add(handler);
    return () => {
      this.handlers.get(event)?.delete(handler);
    };
  }

  onAny(handler: (msg: WSMessage) => void) {
    this.anyHandlers.add(handler);
    return () => {
      this.anyHandlers.delete(handler);
    };
  }

  onReconnect(callback: () => void) {
    this.onReconnectCallbacks.add(callback);
    return () => {
      this.onReconnectCallbacks.delete(callback);
    };
  }

  send(message: WSMessage) {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(message));
    }
  }
}
