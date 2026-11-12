import { createRequire } from "node:module";
import type {
  WebSocket as WsWebSocket,
  WebSocketServer as WsWebSocketServer,
} from "ws";

/**
 * The `ws` constructors, loaded the way the dsh host can actually resolve them.
 *
 * A plain `import { WebSocket, WebSocketServer } from "ws"` fails there twice
 * over: ws ships CommonJS only, so Node's ESM named-export detection does not
 * surface the constructors, and the host resolves bare specifiers with its own
 * conditions, where ws's `browser` export can win — a bare WebSocket class with
 * no WebSocketServer. `require` sidesteps both: no condition set beyond
 * require/node/default, and `module.exports` carries the constructors.
 *
 * Resolution starts at this file, so the compiled `lib/ws.js` finds the `ws` the
 * lane bundle vendors next to `lib/` first, and the checkout's own copy when the
 * plugin runs from source (tests).
 */
const wsModule = createRequire(import.meta.url)("ws") as {
  WebSocket: typeof WsWebSocket;
  WebSocketServer: typeof WsWebSocketServer;
};

export const { WebSocket, WebSocketServer } = wsModule;

/** The instance type a loaded constructor produces (the const above has no type meaning). */
export type WebSocket = WsWebSocket;
