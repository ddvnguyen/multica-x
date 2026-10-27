import { createRoot } from "react-dom/client";
import { ISLAND_CSS } from "../../src/client/popup-seat";
import { IslandApp } from "../../src/client/island-app";
import type { IslandRoute } from "../../src/client/navigation";

/**
 * The S2-D harness: the real island, its real settings page, and a fake memory
 * plane.
 *
 * The Memory page is a browser surface in the two ways jsdom cannot drive:
 * Base UI's picker selects on pointer events, and a record's row is laid out by
 * the island's own stylesheet inside a shadow root. So this page mounts
 * `IslandApp` exactly as the shell does — shadow root, `ISLAND_CSS`, the
 * production portal seat — and answers the seam with a small in-page fake of
 * moct-be that really filters, really versions and really decides.
 *
 * Not part of the shipped client bundle.
 */

const WORKSPACE_ID = "11111111-1111-4111-8111-111111111111";
const AGENT = "22222222-2222-4222-8222-222222222222";
const OTHER_AGENT = "33333333-3333-4333-8333-333333333333";
const ISSUE = "44444444-4444-4444-8444-444444444444";
const WS = `multica:ws:${WORKSPACE_ID}`;

interface FakeRecord {
  id: string;
  workspaceId: string | null;
  type: string;
  title: string;
  body: string;
  attrs: Record<string, unknown>;
  home: { kind: string; ref: string };
  tags: { kind: string; ref: string }[];
  priority: number;
  pinned: boolean;
  status: string;
  flag: string | null;
  version: number;
  tokens: number;
  authorKind: string;
  authorId: string;
  createdAt: string;
  updatedAt: string;
  useCount: number;
  lastUsedAt: string | null;
}

/** A workspace's memory: two records waiting for a human, and three decided. */
const records: FakeRecord[] = [
  {
    id: "mem_rule_pending",
    workspaceId: WS,
    type: "rule",
    title: "Always run the package suite before pushing",
    body: "Run pnpm --filter @multica/dsh-plugin test before pushing a change to the island.",
    attrs: {},
    home: { kind: "expert", ref: `multica:agent:${AGENT}` },
    tags: [{ kind: "expert", ref: `multica:agent:${AGENT}` }],
    priority: 70,
    pinned: false,
    status: "pending",
    flag: null,
    version: 1,
    tokens: 24,
    authorKind: "member",
    authorId: `multica:agent:${AGENT}`,
    createdAt: "2026-10-27T17:45:30.661Z",
    updatedAt: "2026-10-27T17:45:30.661Z",
    useCount: 0,
    lastUsedAt: null,
  },
  {
    id: "mem_decision_pending",
    workspaceId: WS,
    type: "decision",
    title: "M-103 acceptance run",
    body: "M-103 acceptance run: LLM-turn memory_note OK",
    attrs: { decidedBy: "m103-acceptance", sourceRef: `multica:issue:${ISSUE}` },
    home: { kind: "task", ref: `multica:issue:${ISSUE}` },
    tags: [{ kind: "task", ref: `multica:issue:${ISSUE}` }, { kind: "team", ref: WS }],
    priority: 80,
    pinned: false,
    status: "pending",
    flag: null,
    version: 1,
    tokens: 22,
    authorKind: "member",
    authorId: `multica:agent:${AGENT}`,
    createdAt: "2026-10-27T17:45:30.661Z",
    updatedAt: "2026-10-27T17:45:30.661Z",
    useCount: 0,
    lastUsedAt: null,
  },
  {
    id: "mem_convention_active",
    workspaceId: WS,
    type: "convention",
    title: "Lane z keeps its own database",
    body: "Every lane-z run talks to multica_z and its own moct-be; nothing reaches the live control plane.",
    attrs: {},
    home: { kind: "team", ref: WS },
    tags: [{ kind: "team", ref: WS }],
    priority: 60,
    pinned: false,
    status: "active",
    flag: null,
    version: 3,
    tokens: 35,
    authorKind: "member",
    authorId: `multica:agent:${OTHER_AGENT}`,
    createdAt: "2026-10-26T12:56:25.477Z",
    updatedAt: "2026-10-27T09:12:00.000Z",
    useCount: 12,
    lastUsedAt: "2026-10-27T17:48:08.781Z",
  },
  {
    id: "mem_rule_active",
    workspaceId: WS,
    type: "rule",
    title: "Review the diff before merging",
    body: "A seat's memory is a claim; the ledger is the fact. Read the diff, not the summary.",
    attrs: {},
    home: { kind: "expert", ref: `multica:agent:${OTHER_AGENT}` },
    tags: [{ kind: "expert", ref: `multica:agent:${OTHER_AGENT}` }],
    priority: 40,
    pinned: true,
    status: "active",
    flag: null,
    version: 2,
    tokens: 31,
    authorKind: "human",
    authorId: "multica:user:user-1",
    createdAt: "2026-10-25T08:00:00.000Z",
    updatedAt: "2026-10-26T08:00:00.000Z",
    useCount: 5,
    lastUsedAt: "2026-10-27T08:00:00.000Z",
  },
  {
    id: "mem_decision_retracted",
    workspaceId: WS,
    type: "decision",
    title: "Park the flaky browser suite",
    body: "Suggested parking the browser suite until the lane has a headless runner.",
    attrs: { decidedBy: "m104-acceptance", sourceRef: `multica:issue:${ISSUE}` },
    home: { kind: "task", ref: `multica:issue:${ISSUE}` },
    tags: [{ kind: "task", ref: `multica:issue:${ISSUE}` }, { kind: "team", ref: WS }],
    priority: 20,
    pinned: false,
    status: "retracted",
    flag: null,
    version: 2,
    tokens: 19,
    authorKind: "member",
    authorId: `multica:agent:${AGENT}`,
    createdAt: "2026-10-24T08:00:00.000Z",
    updatedAt: "2026-10-25T08:00:00.000Z",
    useCount: 0,
    lastUsedAt: null,
  },
];

const versions: Record<string, unknown[]> = Object.fromEntries(
  records.map((record) => [
    record.id,
    [
      {
        memoryId: record.id,
        version: record.version,
        action: record.version > 1 ? "approve" : "create",
        type: record.type,
        home: record.home,
        status: record.status,
        title: record.title,
        body: record.body,
        actorKind: record.authorKind,
        actorId: record.authorId,
        reason: record.version > 1 ? "reviewed on the lane" : "",
        at: record.updatedAt,
      },
    ],
  ]),
);

export interface FetchCall {
  url: string;
  method: string;
  body: unknown;
}

const calls: FetchCall[] = [];
(window as unknown as { __moctCalls: FetchCall[] }).__moctCalls = calls;

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

/** moct-be's search: the same filters, the same 1..20 page, the same cursor. */
function search(query: URLSearchParams): unknown {
  let matches = records.slice();
  const status = query.get("status");
  if (status !== null && status !== "all") matches = matches.filter((record) => record.status === status);
  const types = (query.get("types") ?? "").split(",").filter((value) => value !== "");
  if (types.length > 0) matches = matches.filter((record) => types.includes(record.type));
  const homes = (query.get("homes") ?? "").split(",").filter((value) => value !== "");
  if (homes.length > 0) matches = matches.filter((record) => homes.includes(record.home.kind));
  const words = (query.get("q") ?? "").toLowerCase().split(/\s+/).filter((word) => word !== "");
  for (const word of words) {
    matches = matches.filter(
      (record) =>
        record.title.toLowerCase().includes(word) || record.body.toLowerCase().includes(word),
    );
  }
  matches.sort((a, b) => b.priority - a.priority || b.updatedAt.localeCompare(a.updatedAt));
  const limit = Math.min(Math.max(Number(query.get("limit") ?? "10"), 1), 20);
  const offset = Number(query.get("cursor") ?? "0");
  const page = matches.slice(offset, offset + limit);
  return {
    results: page,
    total: matches.length,
    nextCursor: offset + page.length < matches.length ? String(offset + page.length) : null,
  };
}

function recordWire(record: FakeRecord): unknown {
  return { ...record, conflictsWith: [], supersedes: null, supersededBy: null, taskRef: null, links: [] };
}

/** The seam's by-id read, plus its two decisions, against the same store. */
function byId(id: string, rest: string, method: string): { status: number; body: unknown } | null {
  const record = records.find((entry) => entry.id === id);
  if (record === undefined) return { status: 404, body: { ok: false, error: "no such memory record" } };
  if (rest === "versions") return { status: 200, body: { versions: versions[id] ?? [] } };
  if (method !== "POST") return { status: 405, body: { ok: false, error: "method not allowed" } };
  if (rest === "approve" || rest === "retract") {
    record.status = rest === "approve" ? "active" : "retracted";
    record.version += 1;
    record.updatedAt = new Date().toISOString();
    (versions[id] as unknown[]).unshift({
      memoryId: id,
      version: record.version,
      action: rest,
      type: record.type,
      home: record.home,
      status: record.status,
      title: record.title,
      body: record.body,
      actorKind: "human",
      actorId: "multica:user:user-1",
      reason: "",
      at: record.updatedAt,
    });
    return { status: 200, body: recordWire(record) };
  }
  return { status: 404, body: { ok: false, error: "not found" } };
}

function answer(url: string, method: string, body: unknown): { status: number; body: unknown } {
  const parsed = new URL(url, location.origin);
  const path = parsed.pathname;
  if (path === "/multica/config") {
    return { status: 200, body: { wsPath: "/multica/ws", workspaceSlug: "demo", problem: null } };
  }
  if (path === "/multica/api/workspaces") {
    return {
      status: 200,
      body: [
        {
          id: WORKSPACE_ID,
          slug: "demo",
          name: "Demo",
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      ],
    };
  }
  if (path === "/multica/api/me") {
    return { status: 200, body: { id: "user-1", name: "Ada", email: "ada@example.test" } };
  }
  if (path === "/multica/api/agents") {
    return {
      status: 200,
      body: [
        { id: AGENT, workspace_id: WORKSPACE_ID, name: "Claude", description: "" },
        { id: OTHER_AGENT, workspace_id: WORKSPACE_ID, name: "DeepSeek", description: "" },
      ],
    };
  }
  if (path === "/multica/api/projects") {
    return { status: 200, body: [{ id: "p-1", title: "Memory plane" }] };
  }
  if (path === "/moct/status") {
    return { status: 200, body: { ok: true, schema: 8, reachable: true } };
  }
  if (path === "/moct/team/v1/memory/usage") {
    return {
      status: 200,
      body: {
        workspaceId: WS,
        capTokens: 2000,
        injectLayerTokens: { min: 200, max: 4000 },
        homes: [
          { home: `team:${WS}`, usedTokens: 640, records: 3, blockedPending: 1, largest: [] },
          { home: `expert:${AGENT}`, usedTokens: 210, records: 2, blockedPending: 1, largest: [] },
        ],
      },
    };
  }
  if (path === "/moct/team/v1/memory" && method === "GET") {
    return { status: 200, body: search(parsed.searchParams) };
  }
  if (path === "/moct/team/v1/memory/get") {
    const ids = (body as { ids?: string[] } | null)?.ids ?? [];
    const found = records.filter((record) => ids.includes(record.id));
    return {
      status: 200,
      body: {
        records: found.map(recordWire),
        missing: ids.filter((id) => !found.some((record) => record.id === id)),
      },
    };
  }
  const byIdMatch = /^\/moct\/team\/v1\/memory\/([^/]+)(?:\/(.*))?$/.exec(path);
  if (byIdMatch !== null) {
    const answer_ = byId(decodeURIComponent(byIdMatch[1] ?? ""), byIdMatch[2] ?? "", method);
    if (answer_ !== null) return answer_;
  }
  // Everything else the fork reads on mount is a list.
  return { status: 200, body: [] };
}

window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
  const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
  const method = init?.method ?? "GET";
  const body: unknown = typeof init?.body === "string" ? JSON.parse(init.body) : null;
  calls.push({ url, method, body });
  const result = answer(url, method, body);
  return json(result.body, result.status);
};

const host = document.getElementById("island-host");
if (host === null) throw new Error("harness requires an #island-host element");

// The island boundary, as `mountIsland` builds it: shadow root, the compiled
// stylesheet, the surface, and a portal seat inside the shadow tree.
const shadow = host.attachShadow({ mode: "open" });
const style = document.createElement("style");
style.dataset.multicaStyles = "";
style.textContent = ISLAND_CSS;
const surface = document.createElement("div");
surface.className = "multica-island-surface";
const rootElement = document.createElement("div");
rootElement.className = "multica-island-root";
const portals = document.createElement("div");
portals.className = "multica-island-portals";
surface.append(rootElement, portals);
shadow.append(style, surface);

const route: IslandRoute = { view: "settings", query: "tab=moct-memory" };

// No StrictMode: `mountIsland` renders the tree once, and a doubled effect
// would make the request assertions below describe the harness, not the panel.
createRoot(rootElement).render(
  <IslandApp
    portalHost={portals}
    preferredSlug="demo"
    wsUrl={`ws://${location.host}/multica/ws`}
    wsPath="/multica/ws"
    problem={null}
    route={route}
  />,
);
