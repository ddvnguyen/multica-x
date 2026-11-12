import type { IncomingMessage, ServerResponse } from "node:http";
import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";
import { sendJson, type MulticaProxySettings } from "./proxy.js";

/**
 * The /moct transport.
 *
 * moct-be owns the memory plane and has no auth: it answers anything that can
 * reach its loopback port, and it trusts the caller's `workspaceId`, `expertId`
 * and `actor`. The browser must hold none of those, so this prefix is the trust
 * boundary — a closed allow-list of memory routes, with the scoping identities
 * injected here from the Multica token the host already holds.
 *
 * Deliberately stricter than `/multica`: that prefix forwards a whole API and
 * lets multica-be answer, while every moct-be route not listed in
 * `matchMoctRoute` is a proxy-side 404 that never reaches the backend.
 */

/** Path prefix dsh reserves for this plugin's memory seam. */
export const MOCT_PREFIX = "/moct";
/** The one host-authored route: status, without moct-be's filesystem paths. */
export const MOCT_STATUS_PATH = `${MOCT_PREFIX}/status`;

/**
 * Record-id shape, as moct-be mints them (`mem_<uuid>`) and as the card pins
 * it: one opaque segment, so an id can never carry a path or a query.
 */
export const MOCT_RECORD_ID = /^[A-Za-z0-9_-]{1,64}$/;

/** A Multica agent id, before it is turned into a `multica:agent:` ref. */
const MULTICA_UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** How long a resolved workspace/actor/role is trusted before it is re-read. */
const IDENTITY_TTL_MS = 60_000;
/** moct-be's own seam timeout; the lane probe answers well inside it. */
const UPSTREAM_TIMEOUT_MS = 10_000;
/** A page of 20 records is tens of KiB; anything near this is a fault. */
const MAX_UPSTREAM_BYTES = 2 * 1024 * 1024;
/** Request bodies here are `{ids}` or `{baseVersion, reason}`. */
const MAX_BODY_BYTES = 64 * 1024;
const MAX_QUERY_TEXT = 256;
const MAX_CURSOR = 256;
const MAX_REASON = 500;

/** The largest page moct-be allows a human reader (`limit must be 1..20`). */
export const MAX_PAGE = 20;

export interface MoctProxySettings {
  /** moct-be origin. Loopback or a private address — never a public host. */
  moctUrl: string;
  /** The Multica server and token the identities are read with. */
  multica: MulticaProxySettings;
}

/**
 * Resolve the memory seam from the plugin row.
 *
 * `moctUrl` (row) or `MOCT_BASE_URL` (host env) turns the seam on; unset is not
 * an error, it means this deployment has no memory plane and `/moct` answers
 * 503 with the reason. A URL that is not http(s) on a loopback/private host is
 * refused for the same reason: the browser is on the other side of this proxy.
 */
export function resolveMoctSettings(
  config: unknown,
  multica: MulticaProxySettings | null,
  env: Record<string, string | undefined> = process.env,
): { settings: MoctProxySettings | null; problem: string | null } {
  if (multica === null) return { settings: null, problem: "multica is not configured" };
  const raw = (config ?? {}) as Record<string, unknown>;
  const rawUrl =
    typeof raw.moctUrl === "string" && raw.moctUrl.trim() !== ""
      ? raw.moctUrl.trim()
      : (env.MOCT_BASE_URL ?? "").trim();
  if (rawUrl === "") return { settings: null, problem: "moctUrl is not set" };
  let url: URL;
  try {
    url = new URL(rawUrl);
  } catch {
    return { settings: null, problem: "moctUrl is not a URL" };
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    return { settings: null, problem: "moctUrl must be http or https" };
  }
  if (!isLoopbackOrPrivate(url.hostname)) {
    return { settings: null, problem: "moctUrl must name a loopback or private host" };
  }
  return {
    settings: { moctUrl: rawUrl.replace(/\/+$/, ""), multica },
    problem: null,
  };
}

/** Whether a host is addressed inside the machine or its own network. */
export function isLoopbackOrPrivate(hostname: string): boolean {
  const host = hostname.toLowerCase().replace(/^\[|\]$/g, "");
  if (host === "localhost" || host.endsWith(".localhost")) return true;
  if (host === "::1") return true;
  const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host);
  if (v4 === null) return false;
  const [a, b] = [Number(v4[1]), Number(v4[2])];
  if (a === 127) return true;
  if (a === 10) return true;
  if (a === 192 && b === 168) return true;
  if (a === 172 && b >= 16 && b <= 31) return true;
  return false;
}

/** One route this prefix serves. */
export type MoctRoute =
  | { kind: "status" }
  | { kind: "list" }
  | { kind: "get" }
  | { kind: "usage" }
  | { kind: "versions"; id: string }
  | { kind: "approve"; id: string }
  | { kind: "retract"; id: string };

/**
 * Match one request against the allow-list.
 *
 * The table is the security boundary, so it is one function rather than a set
 * of prefixes: a route is either named here with its method or it does not
 * exist as far as the browser is concerned. A path that is on the list with the
 * wrong method answers 405, which is a clearer answer than a 404 for the
 * browser's own bug.
 */
export function matchMoctRoute(
  method: string,
  rest: string,
): { route: MoctRoute; reason?: undefined } | { route?: undefined; reason: "not-found" | "method-not-allowed" } {
  const parts = rest.split("/").filter((part) => part !== "");
  const allowed = (kind: MoctRoute["kind"]) => method === (kind === "get" || kind === "approve" || kind === "retract" ? "POST" : "GET");

  if (parts.length === 1 && parts[0] === "status") {
    return allowed("status") ? { route: { kind: "status" } } : { reason: "method-not-allowed" };
  }
  if (parts.length === 3 && parts[0] === "team" && parts[1] === "v1" && parts[2] === "memory") {
    // POST here is moct-be's note route, not this path with the wrong verb: the
    // browser never writes memory itself, so the path does not exist to it.
    return method === "GET" ? { route: { kind: "list" } } : { reason: "not-found" };
  }
  if (parts.length === 4 && parts[0] === "team" && parts[1] === "v1" && parts[2] === "memory") {
    if (parts[3] === "get") return allowed("get") ? { route: { kind: "get" } } : { reason: "method-not-allowed" };
    if (parts[3] === "usage") return allowed("usage") ? { route: { kind: "usage" } } : { reason: "method-not-allowed" };
    return { reason: "not-found" };
  }
  if (parts.length === 5 && parts[0] === "team" && parts[1] === "v1" && parts[2] === "memory") {
    const id = parts[3] ?? "";
    if (!MOCT_RECORD_ID.test(id)) return { reason: "not-found" };
    if (parts[4] === "versions") {
      return allowed("versions") ? { route: { kind: "versions", id } } : { reason: "method-not-allowed" };
    }
    if (parts[4] === "approve") {
      return allowed("approve") ? { route: { kind: "approve", id } } : { reason: "method-not-allowed" };
    }
    if (parts[4] === "retract") {
      return allowed("retract") ? { route: { kind: "retract", id } } : { reason: "method-not-allowed" };
    }
  }
  return { reason: "not-found" };
}

/** A refusal the proxy answers itself, carrying its own status. */
class Refusal extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

/**
 * A refusal that says the configured workspace is gone.
 *
 * A transient multica failure may be served from the last good identity, but a
 * workspace that is no longer readable is not a blip: falling back would scope
 * the whole seam to a workspace the row did not ask for.
 */
class ConfigRefusal extends Refusal {}

/** What one forwarded request came back with. `status: 0` means no answer. */
interface UpstreamAnswer {
  status: number;
  body: Buffer;
}

/** The identities the browser is not allowed to choose. */
interface Identity {
  workspaceUuid: string;
  actorId: string;
}

/** A proxy instance bound to one configuration. */
export class MoctProxy {
  private readonly settings: MoctProxySettings;
  private identityCache: { at: number; value: Identity } | null = null;
  private readonly roleCache = new Map<string, { at: number; role: string | null }>();
  private readonly agentCache = new Map<string, { at: number; ok: boolean }>();
  private readonly log: (message: string) => void;

  constructor(settings: MoctProxySettings, log: (message: string) => void = () => {}) {
    this.settings = settings;
    this.log = log;
  }

  /** Serve one request under the prefix. */
  async handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const url = new URL(req.url ?? "/", "http://dsh.invalid");
    const rest = url.pathname.slice(MOCT_PREFIX.length) || "/";
    const matched = matchMoctRoute(req.method ?? "GET", rest);
    if (matched.route === undefined) {
      if (matched.reason === "method-not-allowed") {
        sendJson(res, 405, { ok: false, error: "method not allowed" });
        return;
      }
      sendJson(res, 404, { ok: false, error: "not found" });
      return;
    }

    // A browser request to this origin has no Origin header; one from another
    // page does, and it is refused even for reads — moct-be has no CORS story.
    if (crossOrigin(req)) {
      sendJson(res, 403, { ok: false, error: "cross-origin request" });
      return;
    }
    if (req.method === "POST" && !isJsonRequest(req)) {
      sendJson(res, 415, { ok: false, error: "content-type must be application/json" });
      return;
    }

    try {
      await this.dispatch(req, res, url, matched.route);
    } catch (error) {
      if (error instanceof Refusal) {
        sendJson(res, error.status, { ok: false, error: error.message });
        return;
      }
      this.log(`moct: unexpected failure: ${String((error as Error).message ?? error)}`);
      sendJson(res, 502, { ok: false, error: "moct proxy failure" });
    }
  }

  private async dispatch(
    req: IncomingMessage,
    res: ServerResponse,
    url: URL,
    route: MoctRoute,
  ): Promise<void> {
    if (route.kind === "status") {
      await this.status(res);
      return;
    }

    const identity = await this.identity();

    if (route.kind === "list") {
      const query = url.searchParams;
      const params = this.scope(identity);
      copyText(query, params, "q", MAX_QUERY_TEXT);
      copyText(query, params, "types", MAX_QUERY_TEXT);
      copyText(query, params, "homes", MAX_QUERY_TEXT);
      copyText(query, params, "status", MAX_QUERY_TEXT);
      copyText(query, params, "cursor", MAX_CURSOR);
      const limit = query.get("limit");
      if (limit !== null && limit !== "") {
        const parsed = Number(limit);
        if (!Number.isSafeInteger(parsed)) throw new Refusal(400, "limit must be a whole number");
        params.set("limit", String(Math.max(1, Math.min(MAX_PAGE, parsed))));
      }
      params.set("expertId", await this.expertRef(identity, query));
      await this.relay(res, "GET", `/team/v1/memory?${params.toString()}`);
      return;
    }

    if (route.kind === "usage") {
      const params = new URLSearchParams({ workspaceId: this.workspaceRef(identity) });
      await this.relay(res, "GET", `/team/v1/memory/usage?${params.toString()}`);
      return;
    }

    if (route.kind === "get") {
      const body = await readJsonBody(req);
      const ids = body["ids"];
      if (!Array.isArray(ids) || ids.length === 0 || ids.length > 10) {
        throw new Refusal(400, "ids must be 1..10 record ids");
      }
      if (!ids.every((id): id is string => typeof id === "string" && MOCT_RECORD_ID.test(id))) {
        throw new Refusal(400, "ids must be 1..10 record ids");
      }
      const params = this.scope(identity);
      params.set("expertId", await this.expertRef(identity, url.searchParams));
      await this.relay(
        res,
        "POST",
        `/team/v1/memory/get?${params.toString()}`,
        JSON.stringify({ ids }),
      );
      return;
    }

    // The by-id routes: the record is read back through the same scoped lookup
    // the browser would use, so an id from another workspace is a 404 here and
    // never reaches a verb.
    const record = await this.record(identity, route.id);

    if (route.kind === "versions") {
      await this.relay(res, "GET", `/team/v1/memory/${encodeURIComponent(route.id)}/versions`);
      return;
    }

    // A record that belongs to no workspace is shared knowledge read from many
    // workspaces; approving it from one of them would decide it for all.
    if (record.workspaceId === null) {
      throw new Refusal(403, "a record outside any workspace cannot be approved or retracted here");
    }
    await this.requireAdmin(identity);

    const body = await readJsonBody(req);
    const baseVersion = body["baseVersion"];
    if (typeof baseVersion !== "number" || !Number.isSafeInteger(baseVersion) || baseVersion < 1) {
      throw new Refusal(400, "baseVersion must be the version you were shown");
    }
    const reason = body["reason"];
    if (reason !== undefined && (typeof reason !== "string" || reason.length > MAX_REASON)) {
      throw new Refusal(400, `reason must be a string of at most ${MAX_REASON} characters`);
    }
    const payload: Record<string, unknown> = {
      baseVersion,
      actor: { kind: "human", id: this.actorRef(identity) },
    };
    if (typeof reason === "string" && reason.trim() !== "") payload["reason"] = reason.trim();
    await this.relay(
      res,
      "POST",
      `/team/v1/memory/${encodeURIComponent(route.id)}/${route.kind}`,
      JSON.stringify(payload),
    );
  }

  /** `/moct/status`: whether moct-be answers, without its filesystem paths. */
  private async status(res: ServerResponse): Promise<void> {
    const answer = await this.upstream("GET", "/team/v1/health", null);
    const reachable = answer.status !== 0 && answer.status !== 504;
    let schema: number | null = null;
    let ok = false;
    if (answer.status === 200) {
      const parsed = parseJsonObject(answer.body);
      ok = parsed?.["ok"] === true;
      const version = parsed?.["schema"];
      if (typeof version === "number" && Number.isSafeInteger(version)) schema = version;
    }
    sendJson(res, 200, { ok, schema, reachable });
  }

  /** The scoped read a by-id route must pass before any verb is forwarded. */
  private async record(identity: Identity, id: string): Promise<{ workspaceId: string | null }> {
    const params = this.scope(identity);
    params.set("expertId", this.actorRef(identity));
    const answer = await this.upstream(
      "POST",
      `/team/v1/memory/get?${params.toString()}`,
      JSON.stringify({ ids: [id] }),
    );
    if (answer.status !== 200) throw new Refusal(answer.status === 0 ? 502 : answer.status, "memory lookup failed");
    const parsed = parseJsonObject(answer.body);
    const records = parsed?.["records"];
    const found = Array.isArray(records) ? records[0] : undefined;
    if (found === undefined || typeof found !== "object" || found === null) {
      throw new Refusal(404, "no such memory record in this workspace");
    }
    const record = found as Record<string, unknown>;
    if (record["id"] !== id) throw new Refusal(404, "no such memory record in this workspace");
    const workspaceId = record["workspaceId"];
    return { workspaceId: typeof workspaceId === "string" ? workspaceId : null };
  }

  /** `expertId`: one agent of this workspace, or the acting human. */
  private async expertRef(identity: Identity, query: URLSearchParams): Promise<string> {
    const agentId = query.get("expertAgentId");
    if (agentId === null || agentId === "") return this.actorRef(identity);
    if (!MULTICA_UUID.test(agentId)) throw new Refusal(400, "expertAgentId must be an agent id");
    if (!(await this.agentInWorkspace(identity, agentId))) {
      throw new Refusal(403, "agent is not in this workspace");
    }
    return `multica:agent:${agentId}`;
  }

  private async agentInWorkspace(identity: Identity, agentId: string): Promise<boolean> {
    const cached = this.agentCache.get(agentId);
    if (cached !== undefined && Date.now() - cached.at < IDENTITY_TTL_MS) return cached.ok;
    const answer = await this.multica("GET", `/api/agents/${agentId}`, identity.workspaceUuid);
    if (answer.status === 0 || answer.status === 504) {
      throw new Refusal(503, "multica did not answer the agent lookup");
    }
    const ok = answer.status === 200;
    this.agentCache.set(agentId, { at: Date.now(), ok });
    return ok;
  }

  /**
   * Approving or retracting is a workspace decision, so the token's human has
   * to be its owner or admin.
   *
   * When the role cannot be read at all (an older server, a members list this
   * token may not see), this is the accepted gap recorded in the PR: the write
   * proceeds because moct-be still enforces its own rules, and the gap is
   * visible rather than silently widening.
   */
  private async requireAdmin(identity: Identity): Promise<void> {
    const role = await this.role(identity);
    if (role === null) {
      this.log("moct: workspace role unavailable; approve/retract not gated on Multica role");
      return;
    }
    if (role !== "owner" && role !== "admin") {
      throw new Refusal(403, "approving memory needs a workspace owner or admin");
    }
  }

  private async role(identity: Identity): Promise<string | null> {
    const cached = this.roleCache.get(identity.workspaceUuid);
    if (cached !== undefined && Date.now() - cached.at < IDENTITY_TTL_MS) return cached.role;
    const answer = await this.multica("GET", `/api/workspaces/${identity.workspaceUuid}/members`, null).catch(
      () => ({ status: 0, body: Buffer.alloc(0) }),
    );
    let role: string | null = null;
    if (answer.status === 200) {
      const members = parseJsonArray(answer.body);
      const mine = members?.find((member) => member["user_id"] === identity.actorId);
      if (typeof mine?.["role"] === "string") role = mine["role"];
    }
    // Only a successful read is cached: a transient failure must not pin the
    // gap for the next minute of writes.
    if (answer.status === 200) {
      this.roleCache.set(identity.workspaceUuid, { at: Date.now(), role });
    }
    return role;
  }

  /**
   * Resolve the workspace and the acting human.
   *
   * The workspace is chosen the way the island's own gate chooses it — the
   * configured slug, else the first readable one — but a configured slug that
   * is not readable is a 503, never a quiet fall back to some other workspace.
   */
  private async identity(): Promise<Identity> {
    if (this.identityCache !== null && Date.now() - this.identityCache.at < IDENTITY_TTL_MS) {
      return this.identityCache.value;
    }
    try {
      const workspaces = await this.multica("GET", "/api/workspaces", null);
      if (workspaces.status !== 200) throw new Refusal(503, "multica did not answer the workspace list");
      const list = parseJsonArray(workspaces.body) ?? [];
      const slug = this.settings.multica.workspaceSlug;
      const chosen =
        slug !== ""
          ? list.find((entry) => entry["slug"] === slug)
          : list[0];
      if (chosen === undefined || typeof chosen["id"] !== "string") {
        throw new ConfigRefusal(
          503,
          slug !== ""
            ? `workspace ${slug} is not readable with this token`
            : "this token cannot read any workspace",
        );
      }
      const me = await this.multica("GET", "/api/me", null);
      if (me.status !== 200) throw new Refusal(503, "multica did not answer the identity lookup");
      const actorId = parseJsonObject(me.body)?.["id"];
      if (typeof actorId !== "string") throw new Refusal(503, "multica did not name the acting user");
      const value: Identity = { workspaceUuid: chosen["id"], actorId };
      this.identityCache = { at: Date.now(), value };
      return value;
    } catch (error) {
      if (error instanceof ConfigRefusal) throw error;
      // A failed refresh keeps the last good identity: a blip must not turn
      // every memory read into a 503 while the values are still valid.
      if (this.identityCache !== null) {
        this.log("moct: identity refresh failed; keeping the last resolved workspace");
        return this.identityCache.value;
      }
      throw error instanceof Refusal ? error : new Refusal(503, "multica is unreachable");
    }
  }

  private workspaceRef(identity: Identity): string {
    return `multica:ws:${identity.workspaceUuid}`;
  }

  private actorRef(identity: Identity): string {
    return `multica:user:${identity.actorId}`;
  }

  /** The scope every read carries: the workspace, a reader, and no tag filter. */
  private scope(identity: Identity): URLSearchParams {
    return new URLSearchParams({
      workspaceId: this.workspaceRef(identity),
      expertId: this.actorRef(identity),
      all: "1",
    });
  }

  /** Forward one request and relay its answer. */
  private async relay(
    res: ServerResponse,
    method: "GET" | "POST",
    path: string,
    body: string | null = null,
  ): Promise<void> {
    const answer = await this.upstream(method, path, body);
    if (answer.status === 0) {
      sendJson(res, 502, { ok: false, error: "moct-be is unreachable" });
      return;
    }
    if (answer.status === 504) {
      sendJson(res, 504, { ok: false, error: "moct-be timed out" });
      return;
    }
    if (answer.status >= 300 && answer.status < 400) {
      // Nothing on this seam redirects; following one would leave the prefix.
      sendJson(res, 502, { ok: false, error: "moct-be answered a redirect" });
      return;
    }
    res.writeHead(answer.status, {
      "content-type": "application/json; charset=utf-8",
      "content-length": answer.body.length,
      // Memory records are workspace data behind a token; no shared cache may
      // hold them, and the type is declared so the browser never sniffs.
      "cache-control": "no-store",
      "x-content-type-options": "nosniff",
    });
    res.end(answer.body);
  }

  /** One request to moct-be. Never rejects: failures are statuses. */
  private upstream(method: string, path: string, body: string | null): Promise<UpstreamAnswer> {
    const headers: Record<string, string> = { accept: "application/json" };
    if (body !== null) {
      headers["content-type"] = "application/json";
      headers["content-length"] = String(Buffer.byteLength(body));
    }
    return call(new URL(`${this.settings.moctUrl}${path}`), method, headers, body);
  }

  /** One request to multica-be, under the host's token. Never rejects. */
  private async multica(
    method: string,
    path: string,
    workspaceUuid: string | null,
  ): Promise<UpstreamAnswer> {
    const headers: Record<string, string> = {
      accept: "application/json",
      authorization: `Bearer ${this.settings.multica.token}`,
    };
    if (workspaceUuid !== null) headers["x-workspace-id"] = workspaceUuid;
    return call(new URL(`${this.settings.multica.serverUrl}${path}`), method, headers, null);
  }
}

/** Whether an `Origin` names somewhere other than this host. */
function crossOrigin(req: IncomingMessage): boolean {
  const origin = req.headers.origin;
  if (typeof origin !== "string" || origin === "") return false;
  const host = req.headers.host;
  if (typeof host !== "string" || host === "") return true;
  try {
    return new URL(origin).host !== host;
  } catch {
    return true;
  }
}

/** Writes are JSON only: a form-encoded body cannot be a `fetch` from a page. */
function isJsonRequest(req: IncomingMessage): boolean {
  const type = req.headers["content-type"];
  return typeof type === "string" && type.toLowerCase().startsWith("application/json");
}

/** Copy one query value, capped, and only when the client actually sent it. */
function copyText(from: URLSearchParams, to: URLSearchParams, name: string, max: number): void {
  const value = from.get(name);
  if (value === null || value === "") return;
  to.set(name, value.slice(0, max));
}

async function readJsonBody(req: IncomingMessage): Promise<Record<string, unknown>> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    const buffer = chunk as Buffer;
    size += buffer.length;
    if (size > MAX_BODY_BYTES) throw new Refusal(413, "body too large");
    chunks.push(buffer);
  }
  if (size === 0) return {};
  try {
    const parsed: unknown = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
      throw new Refusal(400, "body must be a JSON object");
    }
    return parsed as Record<string, unknown>;
  } catch (error) {
    if (error instanceof Refusal) throw error;
    throw new Refusal(400, "body must be JSON");
  }
}

function parseJsonObject(body: Buffer): Record<string, unknown> | null {
  try {
    const parsed: unknown = JSON.parse(body.toString("utf8"));
    return typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : null;
  } catch {
    return null;
  }
}

function parseJsonArray(body: Buffer): Record<string, unknown>[] | null {
  try {
    const parsed: unknown = JSON.parse(body.toString("utf8"));
    if (!Array.isArray(parsed)) return null;
    return parsed.filter(
      (entry): entry is Record<string, unknown> => typeof entry === "object" && entry !== null,
    );
  } catch {
    return null;
  }
}

/**
 * One HTTP call with a timeout and a size cap.
 *
 * Redirects are not followed: the answer is handed back as a 3xx for the caller
 * to refuse. `status: 0` means the transport failed, `504` means it timed out —
 * neither is thrown, because every caller answers the browser with a status.
 */
function call(
  url: URL,
  method: string,
  headers: Record<string, string>,
  body: string | null,
): Promise<UpstreamAnswer> {
  return new Promise((resolve) => {
    const request = url.protocol === "https:" ? httpsRequest : httpRequest;
    let settled = false;
    const finish = (answer: UpstreamAnswer): void => {
      if (settled) return;
      settled = true;
      resolve(answer);
    };

    const upstream = request({
      protocol: url.protocol,
      hostname: url.hostname,
      port: url.port === "" ? undefined : Number(url.port),
      path: `${url.pathname}${url.search}`,
      method,
      headers,
    });

    const timer = setTimeout(() => {
      upstream.destroy();
      finish({ status: 504, body: Buffer.from(JSON.stringify({ ok: false, error: "timeout" })) });
    }, UPSTREAM_TIMEOUT_MS);

    upstream.on("response", (response) => {
      const chunks: Buffer[] = [];
      let size = 0;
      let overflow = false;
      response.on("data", (chunk: Buffer) => {
        if (overflow) return;
        size += chunk.length;
        if (size > MAX_UPSTREAM_BYTES) {
          overflow = true;
          clearTimeout(timer);
          response.destroy();
          finish({ status: 502, body: Buffer.from(JSON.stringify({ ok: false, error: "oversized answer" })) });
          return;
        }
        chunks.push(chunk);
      });
      response.on("end", () => {
        clearTimeout(timer);
        finish({ status: response.statusCode ?? 502, body: Buffer.concat(chunks) });
      });
      response.on("error", () => {
        clearTimeout(timer);
        finish({ status: 0, body: Buffer.alloc(0) });
      });
    });
    upstream.on("error", () => {
      clearTimeout(timer);
      finish({ status: 0, body: Buffer.alloc(0) });
    });
    upstream.end(body ?? undefined);
  });
}
