/** Where the host half answers the island's boot questions. */
export const ISLAND_CONFIG_PATH = "/multica/config";

export interface IslandConfig {
  /** Same-origin path of the realtime channel. */
  wsPath: string;
  /** Workspace the panel opens; empty selects the first readable one. */
  workspaceSlug: string;
  /** Why the host cannot proxy, when it cannot. Never a credential. */
  problem: string | null;
}

export const DEFAULT_ISLAND_CONFIG: IslandConfig = {
  wsPath: "/multica/ws",
  workspaceSlug: "",
  problem: null,
};

/**
 * Read the host's boot config.
 *
 * The host owns the workspace choice and the WebSocket path, and neither is a
 * secret; the token is deliberately absent from this payload.
 */
export async function loadIslandConfig(
  fetchImpl: typeof fetch = fetch,
): Promise<IslandConfig> {
  const response = await fetchImpl(ISLAND_CONFIG_PATH, {
    headers: { accept: "application/json" },
    credentials: "same-origin",
  });
  if (!response.ok) {
    throw new Error(`island config responded ${response.status}`);
  }
  const body = (await response.json()) as Partial<IslandConfig> | null;
  return {
    wsPath:
      typeof body?.wsPath === "string" && body.wsPath.startsWith("/")
        ? body.wsPath
        : DEFAULT_ISLAND_CONFIG.wsPath,
    workspaceSlug:
      typeof body?.workspaceSlug === "string" ? body.workspaceSlug : "",
    problem: typeof body?.problem === "string" ? body.problem : null,
  };
}

/** Absolute WebSocket URL for a same-origin path. */
export function islandWebSocketUrl(wsPath: string, location: Location): string {
  const protocol = location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${location.host}${wsPath}`;
}
