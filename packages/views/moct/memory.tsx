// These screens are an existing English-only MOCT surface moved into views;
// preserve its copy and behavior during the shared-package migration.
/* eslint-disable i18next/no-literal-string, no-restricted-syntax */
import { useCallback, useEffect, useMemo, useState } from "react";
import { Input } from "@multica/ui/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { SettingsCard, SettingsRow, SettingsSection, SettingsTab } from "../settings";
import {
  ActionButton,
  callJson,
  Failure,
  moctStatusPath,
  moctUsagePath,
  moctWorkspacePath,
  parseMoctStatus,
  parseMoctUsage,
  RefreshButton,
  Status,
  text,
  useSeamRead,
  wholeNumber,
  type Load,
  type MoctConnection,
} from "./seam";

/**
 * The Memory entry of the MOCT group (S2-D): the workspace's memory records,
 * the two decisions a human makes about them, and the two owner writes.
 *
 * Three things this page is not. It is not an agent writer: agents write
 * through `memory_note`, and the verbs here are moct-be's `approve` and
 * `retract` plus the owner's own `create` and `purge`. It is not an editor:
 * no title, body, home or pin is editable, because a record's text is the
 * writer's, not the reader's. And it is not a second authority: every
 * listing, version row, decision and write is moct-be's answer, relayed
 * through the authenticated workspace API, which owns the workspace and the
 * acting human's ref. The browser still chooses nothing but the filters and
 * the note it files.
 */

/** The types moct accepts on a write and on a `types` filter. */
export const MEMORY_TYPES = [
  "rule",
  "decision",
  "convention",
  "how_to_work",
  "task",
  "branch",
] as const;
export type MemoryType = (typeof MEMORY_TYPES)[number];

/** The homes moct accepts on a `homes` filter: where a record is filed. */
export const MEMORY_HOMES = ["task", "channel", "team", "expert"] as const;
export type MemoryHomeKind = (typeof MEMORY_HOMES)[number];

/** The statuses moct stores. `all` is a filter value, never a stored one. */
export const MEMORY_STATUSES = [
  "pending",
  "active",
  "superseded",
  "retracted",
  "expired",
] as const;
export type MemoryStatus = (typeof MEMORY_STATUSES)[number];
export type MemoryStatusChoice = MemoryStatus | "all";

/** moct's own read bound (`limit must be 1..20`); the host clamps to it too. */
export const MEMORY_PAGE_SIZE = 20;

export interface MemoryHome {
  kind: string;
  ref: string;
}

export interface MemoryTag {
  kind: string;
  ref: string;
}

/** One record link, as `POST /memory/get` sends it beside the record. */
export interface MemoryLink {
  kind: string;
  id: string;
  type: string;
  title: string | null;
}

/**
 * One memory record.
 *
 * `type`, `status` and `home.kind` are kept as moct sent them rather than
 * narrowed to the vocabularies above: the filter controls offer the vocabulary,
 * but a record already in the store must stay visible and labelled even if this
 * build has never heard of its type.
 */
export interface MemoryRecord {
  id: string;
  workspaceId: string | null;
  type: string;
  title: string | null;
  body: string | null;
  attrs: Record<string, unknown> | null;
  home: MemoryHome;
  tags: MemoryTag[];
  priority: number;
  pinned: boolean;
  status: string;
  /** `blocked_budget`: approved, but its home's always-load set is full. */
  flag: string | null;
  supersedes: string | null;
  supersededBy: string | null;
  conflictsWith: string[];
  taskRef: string | null;
  authorKind: string;
  authorId: string;
  createdAt: string;
  updatedAt: string;
  lastUsedAt: string | null;
  useCount: number;
  version: number;
  tokens: number;
  /** Present only on a `get`, which is the one read that also sends links. */
  links?: MemoryLink[];
}

/** One version row of a record's history. */
export interface MemoryVersion {
  memoryId: string;
  version: number;
  action: string;
  type: string;
  home: MemoryHome;
  status: string;
  title: string | null;
  body: string | null;
  actorKind: string;
  actorId: string;
  reason: string;
  at: string;
}

export interface MemoryPage {
  records: MemoryRecord[];
  total: number;
  nextCursor: string | null;
}

export interface MemoryGet {
  records: MemoryRecord[];
  missing: string[];
}

/** The filters this page offers; every one is a moct query parameter. */
export interface MemoryFilters {
  q: string;
  type: string;
  status: MemoryStatusChoice;
  home: string;
  /** Agent whose records to show. Narrowed here, not at moct (see below). */
  agent: string;
}

/**
 * What the page opens on.
 *
 * Every status the workspace stores: the queue that needs a human is one
 * choice away, behind "Waiting for approval".
 */
export const MEMORY_DEFAULT_FILTERS: MemoryFilters = {
  q: "",
  type: "",
  status: "all",
  home: "",
  agent: "",
};

export function moctMemoryPath(connection: MoctConnection): string {
  return `${moctWorkspacePath(connection)}/team/v1/memory`;
}

/** The labels a reader sees, keyed by the value moct stores. */
const TYPE_LABELS: Record<string, string> = {
  rule: "Rule",
  decision: "Decision",
  convention: "Convention",
  how_to_work: "How to work",
  task: "Task",
  branch: "Branch",
};

const HOME_LABELS: Record<string, string> = {
  team: "Workspace",
  expert: "Agent",
  channel: "Project",
  task: "Issue",
};

const STATUS_LABELS: Record<string, string> = {
  pending: "Waiting for approval",
  active: "Active",
  superseded: "Superseded",
  retracted: "Retracted",
  expired: "Expired",
};

const STATUS_CHOICES: readonly { value: MemoryStatusChoice; label: string }[] = [
  { value: "pending", label: STATUS_LABELS["pending"] as string },
  { value: "active", label: STATUS_LABELS["active"] as string },
  { value: "superseded", label: STATUS_LABELS["superseded"] as string },
  { value: "retracted", label: STATUS_LABELS["retracted"] as string },
  { value: "expired", label: STATUS_LABELS["expired"] as string },
  { value: "all", label: "All" },
];

/** A vocabulary member's label, and an unknown value told as it is. */
export function typeLabel(type: string): string {
  return TYPE_LABELS[type] ?? type;
}

export function homeLabel(kind: string): string {
  return HOME_LABELS[kind] ?? kind;
}

export function statusLabel(status: string): string {
  return STATUS_LABELS[status] ?? status;
}

function asRecord(value: unknown): Record<string, unknown> {
  return typeof value === "object" && value !== null
    ? (value as Record<string, unknown>)
    : {};
}

function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function num(value: unknown): number {
  return wholeNumber(value) ?? 0;
}

function flag(value: unknown): boolean {
  return value === true;
}

function list(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function memoryHome(value: unknown): MemoryHome {
  const home = asRecord(value);
  return { kind: str(home["kind"]), ref: str(home["ref"]) };
}

function memoryTags(value: unknown): MemoryTag[] {
  return list(value).flatMap((entry) => {
    const tag = asRecord(entry);
    const kind = str(tag["kind"]);
    if (kind === "") return [];
    return [{ kind, ref: str(tag["ref"]) }];
  });
}

function stringList(value: unknown): string[] {
  return list(value).filter((entry): entry is string => typeof entry === "string");
}

function attrs(value: unknown): Record<string, unknown> | null {
  const record = asRecord(value);
  return Object.keys(record).length === 0 ? null : record;
}

/**
 * One record, or null when the answer is not one.
 *
 * A record without an id is not addressable — it can be neither versioned nor
 * decided — so it is dropped rather than rendered as a row that cannot open.
 */
export function parseMemoryRecord(body: unknown): MemoryRecord | null {
  const record = asRecord(body);
  const id = str(record["id"]);
  if (id === "") return null;
  return {
    id,
    workspaceId: text(record["workspaceId"]) ?? null,
    type: str(record["type"]),
    title: text(record["title"]) ?? null,
    body: typeof record["body"] === "string" ? record["body"] : null,
    attrs: attrs(record["attrs"]),
    home: memoryHome(record["home"]),
    tags: memoryTags(record["tags"]),
    priority: num(record["priority"]),
    pinned: flag(record["pinned"]),
    status: str(record["status"]),
    flag: text(record["flag"]) ?? null,
    supersedes: text(record["supersedes"]) ?? null,
    supersededBy: text(record["supersededBy"]) ?? null,
    conflictsWith: stringList(record["conflictsWith"]),
    taskRef: text(record["taskRef"]) ?? null,
    authorKind: str(record["authorKind"]),
    authorId: str(record["authorId"]),
    createdAt: str(record["createdAt"]),
    updatedAt: str(record["updatedAt"]),
    lastUsedAt: text(record["lastUsedAt"]) ?? null,
    useCount: num(record["useCount"]),
    version: num(record["version"]),
    tokens: num(record["tokens"]),
  };
}

/** One note answer: moct-be's `{record}` envelope, or `{record:null}` held for confirmation. */
export interface MemoryNoteAnswer {
  record: MemoryRecord | null;
  held: boolean;
}

/**
 * One note answer, or null when the answer is not one.
 *
 * A held write (`record` null with near-matches) is not a record — the reader
 * must confirm, and this page's form has no confirmNew path, so it reports
 * the hold rather than rendering a row that does not exist.
 */
export function parseMemoryNote(body: unknown): MemoryNoteAnswer | null {
  const envelope = asRecord(body);
  if (!("record" in envelope)) return null;
  const raw = envelope["record"];
  if (raw === null) return { record: null, held: true };
  const parsed = parseMemoryRecord(raw);
  return parsed === null ? null : { record: parsed, held: false };
}

/** One search answer: `{results,total,nextCursor}`. */
export function parseMemoryPage(body: unknown): MemoryPage {
  const record = asRecord(body);
  const records = list(record["results"]).flatMap((entry) => {
    const parsed = parseMemoryRecord(entry);
    return parsed === null ? [] : [parsed];
  });
  return {
    records,
    total: num(record["total"]),
    nextCursor: text(record["nextCursor"]) ?? null,
  };
}

/** One `get` answer: `{records,missing}`, each record carrying its links. */
export function parseMemoryGet(body: unknown): MemoryGet {
  const record = asRecord(body);
  const records = list(record["records"]).flatMap((entry) => {
    const parsed = parseMemoryRecord(entry);
    if (parsed === null) return [];
    return [
      {
        ...parsed,
        links: list(asRecord(entry)["links"]).flatMap((link) => {
          const raw = asRecord(link);
          const id = str(raw["id"]);
          if (id === "") return [];
          return [
            {
              kind: str(raw["kind"]),
              id,
              type: str(raw["type"]),
              title: text(raw["title"]) ?? null,
            },
          ];
        }),
      },
    ];
  });
  return { records, missing: stringList(record["missing"]) };
}

/** One versions answer: `{versions:[…]}`. */
export function parseMemoryVersions(body: unknown): MemoryVersion[] {
  const record = asRecord(body);
  return list(record["versions"]).flatMap((entry) => {
    const version = asRecord(entry);
    const memoryId = str(version["memoryId"]);
    if (memoryId === "") return [];
    return [
      {
        memoryId,
        version: num(version["version"]),
        action: str(version["action"]),
        type: str(version["type"]),
        home: memoryHome(version["home"]),
        status: str(version["status"]),
        title: text(version["title"]) ?? null,
        body: typeof version["body"] === "string" ? version["body"] : null,
        actorKind: str(version["actorKind"]),
        actorId: str(version["actorId"]),
        reason: str(version["reason"]),
        at: str(version["at"]),
      },
    ];
  });
}

/** The list URL for one filter set and page. */
export function memoryListPath(filters: MemoryFilters, cursor: string | null, connection: MoctConnection): string {
  const params = new URLSearchParams({ limit: String(MEMORY_PAGE_SIZE) });
  const q = filters.q.trim();
  if (q !== "") params.set("q", q);
  if (filters.type !== "") params.set("types", filters.type);
  if (filters.home !== "") params.set("homes", filters.home);
  params.set("status", filters.status);
  if (cursor !== null && cursor !== "") params.set("cursor", cursor);
  return `${moctMemoryPath(connection)}?${params.toString()}`;
}

export function memoryRecordPath(id: string, connection: MoctConnection): string {
  return `${moctMemoryPath(connection)}/${encodeURIComponent(id)}`;
}

export function memoryGetPath(connection: MoctConnection): string {
  return `${moctMemoryPath(connection)}/get`;
}

export function memoryVersionsPath(id: string, connection: MoctConnection): string {
  return `${memoryRecordPath(id, connection)}/versions`;
}

/** The create URL for one owner's note: a POST on the memory collection. */
export function memoryCreatePath(connection: MoctConnection): string {
  return `${moctMemoryPath(connection)}`;
}

/** An owner's note, as the seam's create route takes it. */
export interface MemoryNoteInput {
  type: string;
  title: string;
  body: string;
  homeKind: string;
  homeRef: string;
  attrs?: Record<string, unknown>;
}

/** `purge` — the seam's permanent delete, and its only delete verb. */
export function memoryPurgePath(id: string, connection: MoctConnection): string {
  return `${memoryRecordPath(id, connection)}/purge`;
}

/** `approve` or `retract` — the seam's two decisions, and its two other verbs. */
export function memoryDecisionPath(id: string, verb: "approve" | "retract", connection: MoctConnection): string {
  return `${memoryRecordPath(id, connection)}/${verb}`;
}

/** An agent's ref as moct writes it: the profile a record can be homed on. */
export function agentRef(agentId: string): string {
  return `multica:agent:${agentId}`;
}

/**
 * Whether a record belongs to one agent.
 *
 * moct has no per-agent filter: a read is scoped by workspace and by the
 * reader, and every `decision` carries a team tag, so narrowing by expert at
 * moct would not narrow. What this page can answer honestly is "which of the
 * records I have loaded name this agent" — the profile it is homed on, the
 * expert tag it carries, or the writer.
 */
export function recordNamesAgent(record: MemoryRecord, ref: string): boolean {
  if (ref === "") return true;
  if (record.home.kind === "expert" && record.home.ref === ref) return true;
  if (record.tags.some((tag) => tag.kind === "expert" && tag.ref === ref)) return true;
  return record.authorId === ref;
}

/** The records the page shows: every loaded one, or one agent's. */
export function visibleRecords(
  records: readonly MemoryRecord[],
  agentId: string,
): MemoryRecord[] {
  const ref = agentId === "" ? "" : agentRef(agentId);
  return records.filter((record) => recordNamesAgent(record, ref));
}

/** One directory entry this page can label a ref with. */
export interface MemoryDirectoryEntry {
  id: string;
  name: string;
}

export interface MemoryDirectory {
  agents: readonly MemoryDirectoryEntry[];
  projects: readonly MemoryDirectoryEntry[];
  /** What a team home is called: the workspace the panel speaks as. */
  team: string;
}

function parseDirectory(body: unknown, entries: (value: unknown) => unknown[]): MemoryDirectoryEntry[] {
  const raw = Array.isArray(body) ? body : entries(body);
  return raw.flatMap((entry) => {
    const record = asRecord(entry);
    const id = str(record["id"]);
    if (id === "") return [];
    return [{ id, name: text(record["name"]) ?? text(record["title"]) ?? id }];
  });
}

/**
 * The names a home ref is shown with.
 *
 * Both lists come from Multica (the workspace's agents and projects), not from
 * moct: moct knows refs and never names. A list that cannot be read leaves its
 * refs shown as moct sent them — a missing label is not a reason to hide a
 * record, and neither list is authoritative for anything on this page.
 */
export function useMemoryDirectory(workspaceId: string, workspaceSlug: string, apiBasePath: string): MemoryDirectory {
  const [directory, setDirectory] = useState<MemoryDirectory>({
    agents: [],
    projects: [],
    team: workspaceSlug === "" ? "This workspace" : workspaceSlug,
  });

  useEffect(() => {
    if (workspaceId === "") return;
    let cancelled = false;
    const read = async (path: string, parse: (body: unknown) => MemoryDirectoryEntry[]) => {
      const answer = await callJson(path, parse).catch(() => null);
      return answer !== null && answer.ok && answer.parsed !== null ? answer.parsed : [];
    };
    void Promise.all([
      read(
        `${apiBasePath}/api/agents?workspace_id=${encodeURIComponent(workspaceId)}`,
        (body) => parseDirectory(body, () => []),
      ),
      read(`${apiBasePath}/api/projects`, (body) =>
        parseDirectory(body, (value) => list(asRecord(value)["projects"])),
      ),
    ]).then(([agents, projects]) => {
      if (!cancelled) {
        setDirectory((current) => ({ ...current, agents, projects }));
      }
    });
    return () => {
      cancelled = true;
    };
  }, [workspaceId, apiBasePath]);

  return directory;
}

/** The name a home ref is shown with, or the ref's own tail when unresolved. */
export function homeName(home: MemoryHome, directory: MemoryDirectory): string {
  if (home.kind === "team") return directory.team;
  const names: Record<string, readonly MemoryDirectoryEntry[]> = {
    expert: directory.agents,
    channel: directory.projects,
  };
  const match = (names[home.kind] ?? []).find(
    (entry) => home.ref === agentRef(entry.id) || home.ref.endsWith(entry.id),
  );
  if (match !== undefined) return match.name;
  const tail = home.ref.split(":").pop() ?? home.ref;
  return tail === "" ? home.ref : tail.slice(0, 8);
}

/** One dimension a record's identity is made of, rendered as a chip. */
function Chip({ label, value }: { label?: string; value: string }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 text-caption text-muted-foreground">
      {label === undefined ? null : <span className="opacity-70">{label}</span>}
      <span>{value}</span>
    </span>
  );
}

/** The record's status, as moct stores it, with the page's own words. */
function StatusChip({ status }: { status: string }) {
  return (
    <span data-memory-status={status}>
      <Chip value={statusLabel(status)} />
    </span>
  );
}

function successNotice(verb: "approve" | "retract"): string {
  return verb === "approve"
    ? "Approved. The record injects from here on."
    : "Rejected. The record stays in the store and does not inject.";
}

/**
 * One record's page: what it says, where it lives, its versions, and — while it
 * is pending — the two decisions.
 *
 * The list's copy renders first so the page never opens empty, and the seam's
 * own `get` replaces it: the version a decision must carry is the version the
 * reader was shown, and the list's copy can be a page old.
 */
function RecordDetail({
  id,
  summary,
  directory,
  connection,
  canManage,
  onBack,
  onDecided,
  onPurged,
}: {
  id: string;
  summary: MemoryRecord;
  directory: MemoryDirectory;
  connection: MoctConnection;
  canManage: boolean;
  onBack: () => void;
  onDecided: (record: MemoryRecord) => void;
  onPurged: (id: string) => void;
}) {
  const [record, setRecord] = useState<MemoryRecord>(summary);
  const [versions, setVersions] = useState<Load<MemoryVersion[]>>({ state: "loading" });
  const [links, setLinks] = useState<MemoryLink[]>(summary.links ?? []);
  const [failure, setFailure] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [attempt, setAttempt] = useState(0);

  const reload = useCallback(() => setAttempt((current) => current + 1), []);

  useEffect(() => {
    let cancelled = false;
    setVersions({ state: "loading" });
    void callJson(memoryVersionsPath(id, connection), parseMemoryVersions).then((answer) => {
      if (cancelled) return;
      setVersions(
        answer.ok && answer.parsed !== null
          ? { state: "ready", value: answer.parsed }
          : { state: "error", message: answer.message ?? "the server did not answer" },
      );
    });
    void callJson(memoryGetPath(connection), parseMemoryGet, {
      method: "POST",
      body: { ids: [id] },
    }).then((answer) => {
      if (cancelled) return;
      const fresh = answer.parsed?.records[0];
      if (fresh === undefined) return;
      setRecord(fresh);
      setLinks(fresh.links ?? []);
    });
    return () => {
      cancelled = true;
    };
  }, [id, attempt, connection]);

  const decide = useCallback(
    async (verb: "approve" | "retract") => {
      setBusy(true);
      setFailure(null);
      setNotice(null);
      // The base version is the one the reader was shown; a record that moved
      // underneath is moct's 409, and the reload below is what fixes it.
      const answer = await callJson(
        memoryDecisionPath(id, verb, connection),
        parseMemoryRecord,
        { method: "POST", body: { baseVersion: record.version } },
      );
      setBusy(false);
      if (!answer.ok || answer.parsed === null) {
        setFailure(answer.message ?? "the server did not answer");
        reload();
        return;
      }
      const decided = answer.parsed;
      setRecord(decided);
      setNotice(successNotice(verb));
      onDecided(decided);
      reload();
    },
    [id, record.version, onDecided, reload, connection],
  );

  const [confirmingPurge, setConfirmingPurge] = useState(false);
  const purge = useCallback(async () => {
    setBusy(true);
    setFailure(null);
    setNotice(null);
    const answer = await callJson(memoryPurgePath(id, connection), parseMemoryRecord, {
      method: "POST",
      body: {},
    });
    setBusy(false);
    setConfirmingPurge(false);
    if (!answer.ok || answer.parsed === null) {
      setFailure(answer.message ?? "the server did not answer");
      reload();
      return;
    }
    onPurged(id);
    onBack();
  }, [id, connection, onPurged, onBack, reload]);

  const title = record.title ?? "Untitled record";
  const pending = record.status === "pending";
  // A record that belongs to no workspace is shared knowledge, read from many
  // workspaces: deciding it from this one would decide it for all of them, and
  // the seam refuses it. So the button is not offered at all.
  const decidable = canManage && pending && record.workspaceId !== null;

  return (
    <SettingsTab
      title={title}
      breadcrumb={
        <button
          type="button"
          onClick={onBack}
          className="inline-flex items-center gap-1 text-caption font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
        >
          Memory
        </button>
      }
      description={
        <span className="flex flex-wrap items-center gap-1.5">
          <Chip label="Type" value={typeLabel(record.type)} />
          <StatusChip status={record.status} />
          <Chip label="Home" value={`${homeLabel(record.home.kind)} · ${homeName(record.home, directory)}`} />
          {record.flag === null ? null : <Chip label="Flag" value={record.flag} />}
        </span>
      }
      actions={
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={onBack}
            className="rounded-md px-2 py-1 text-label font-medium text-muted-foreground hover:bg-muted/50 hover:text-foreground"
          >
            Back to Memory
          </button>
          <RefreshButton onClick={reload} />
        </div>
      }
    >
      <SettingsSection
        title={pending ? "Decision" : "Decided"}
        description={
          decidable
            ? "Approving lets the record inject; rejecting retracts it. Nothing here edits the text — that belongs to the writer."
            : pending
              ? "This record belongs to no workspace, so it is decided where it is homed, not from here."
              : "This record is no longer waiting for a decision."
        }
      >
        <SettingsCard>
          <SettingsRow label="Status">
            <span className="flex items-center gap-2">
              <StatusChip status={record.status} />
              <Status value={`version ${record.version}`} />
            </span>
          </SettingsRow>
          {decidable ? (
            <SettingsRow label="Approve or reject">
              <span className="flex items-center gap-2">
                <ActionButton
                  label={`Approve ${title}`}
                  disabled={busy}
                  onClick={() => void decide("approve")}
                >
                  <span data-memory-action="approve">Approve</span>
                </ActionButton>
                <ActionButton
                  label={`Reject ${title}`}
                  disabled={busy}
                  onClick={() => void decide("retract")}
                >
                  <span data-memory-action="retract">Reject</span>
                </ActionButton>
              </span>
            </SettingsRow>
          ) : null}
          {canManage && record.workspaceId !== null ? (
            <SettingsRow
              label="Delete"
              description="Permanent. Purged text is gone and cannot be restored — retract instead to keep it in the store without injecting."
            >
              {confirmingPurge ? (
                <span className="flex items-center gap-2">
                  <ActionButton
                    label={`Delete ${title} permanently`}
                    disabled={busy}
                    onClick={() => void purge()}
                  >
                    <span data-memory-action="purge-confirm">Delete permanently</span>
                  </ActionButton>
                  <ActionButton label="Keep this record" disabled={busy} onClick={() => setConfirmingPurge(false)}>
                    Keep
                  </ActionButton>
                </span>
              ) : (
                <ActionButton
                  label={`Delete ${title}`}
                  disabled={busy}
                  onClick={() => setConfirmingPurge(true)}
                >
                  <span data-memory-action="purge">Delete…</span>
                </ActionButton>
              )}
            </SettingsRow>
          ) : null}
          {notice === null ? null : (
            <SettingsRow label="Last decision">
              <span role="status" className="text-body text-muted-foreground">
                {notice}
              </span>
            </SettingsRow>
          )}
          {failure === null ? null : (
            <SettingsRow label="Last decision">
              <Failure message={failure} />
            </SettingsRow>
          )}
        </SettingsCard>
      </SettingsSection>

      <SettingsSection title="Record">
        <SettingsCard>
          <SettingsRow label="Home" description={record.home.ref}>
            <Status value={homeLabel(record.home.kind)} />
          </SettingsRow>
          <SettingsRow label="Written by" description={record.authorId}>
            <Status value={record.authorKind} />
          </SettingsRow>
          <SettingsRow label="Priority">
            <Status value={record.pinned ? `${record.priority} · pinned` : String(record.priority)} />
          </SettingsRow>
          <SettingsRow label="Always-load">
            <Status
              value={
                record.flag === "blocked_budget"
                  ? `${record.tokens} tokens · held: this home's always-load set is full`
                  : `${record.tokens} tokens`
              }
            />
          </SettingsRow>
          <SettingsRow label="Written" description={`last changed ${record.updatedAt}`}>
            <Status value={record.createdAt} />
          </SettingsRow>
          <SettingsRow label="Used">
            <Status value={`${record.useCount} times${record.lastUsedAt === null ? "" : ` · last ${record.lastUsedAt}`}`} />
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>

      <SettingsSection title="Body">
        <SettingsCard>
          {record.body === null || record.body.trim() === "" ? (
            <SettingsRow label="Text">
              <Status value="This record has no body" />
            </SettingsRow>
          ) : (
            <div className="px-4 py-3.5">
              <pre
                data-memory-body
                className="whitespace-pre-wrap break-words font-sans text-body text-foreground"
              >
                {record.body}
              </pre>
            </div>
          )}
        </SettingsCard>
      </SettingsSection>

      {record.attrs === null ? null : (
        <SettingsSection title="Attributes">
          <SettingsCard>
            {Object.entries(record.attrs).map(([key, value]) => (
              <SettingsRow key={key} label={key} align="start">
                <pre className="whitespace-pre-wrap break-words text-body text-muted-foreground">
                  {typeof value === "string" ? value : JSON.stringify(value)}
                </pre>
              </SettingsRow>
            ))}
          </SettingsCard>
        </SettingsSection>
      )}

      {links.length === 0 ? null : (
        <SettingsSection title="Linked records">
          <SettingsCard>
            {links.map((link) => (
              <SettingsRow key={`${link.kind}:${link.id}`} label={link.title ?? link.id} description={link.kind}>
                <Status value={typeLabel(link.type)} />
              </SettingsRow>
            ))}
          </SettingsCard>
        </SettingsSection>
      )}

      <SettingsSection
        title="Versions"
        description="Every write and every decision, newest first."
        action={<RefreshButton onClick={reload} />}
      >
        <SettingsCard>
          {versions.state === "loading" ? (
            <SettingsRow label="History">
              <Status value="Loading…" />
            </SettingsRow>
          ) : versions.state === "error" ? (
            <SettingsRow label="History">
              <span className="flex items-center gap-2">
                <Failure message={versions.message} />
                <ActionButton label="Retry loading the versions" onClick={reload}>
                  Retry
                </ActionButton>
              </span>
            </SettingsRow>
          ) : versions.value.length === 0 ? (
            <SettingsRow label="History">
              <Status value="This record has no versions" />
            </SettingsRow>
          ) : (
            <ul data-memory-versions className="divide-y divide-surface-border">
              {versions.value.map((version) => (
                <li key={version.version} className="px-4 py-3">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <Chip value={`v${version.version}`} />
                    <Chip value={version.action} />
                    <StatusChip status={version.status} />
                    <Status value={`${version.actorKind} · ${version.actorId} · ${version.at}`} />
                  </div>
                  {version.reason === "" ? null : (
                    <p className="mt-1 text-caption text-muted-foreground">{version.reason}</p>
                  )}
                </li>
              ))}
            </ul>
          )}
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}

/** One record as a row: what it is, where it lives, and the way into it. */
function RecordRow({
  record,
  directory,
  onOpen,
}: {
  record: MemoryRecord;
  directory: MemoryDirectory;
  onOpen: () => void;
}) {
  const title = record.title ?? "Untitled record";
  return (
    <SettingsRow
      anchor={record.id}
      align="start"
      label={
        <span data-memory-record={record.id} className="text-body font-medium">
          {title}
        </span>
      }
      description={
        <span className="flex flex-wrap items-center gap-1.5">
          <Chip label="Type" value={typeLabel(record.type)} />
          <StatusChip status={record.status} />
          <Chip label="Home" value={`${homeLabel(record.home.kind)} · ${homeName(record.home, directory)}`} />
          {record.flag === null ? null : <Chip label="Flag" value={record.flag} />}
          <Status value={record.updatedAt} />
        </span>
      }
    >
      <ActionButton label={`Open ${title}`} onClick={onOpen}>
        Open
      </ActionButton>
    </SettingsRow>
  );
}

/** The filter controls, each one a moct query parameter or a page-local one. */
function Filters({
  filters,
  directory,
  busy,
  onApply,
}: {
  filters: MemoryFilters;
  directory: MemoryDirectory;
  busy: boolean;
  onApply: (next: Partial<MemoryFilters>) => void;
}) {
  // The search box holds text until it is submitted: a request per keystroke
  // would query moct for every prefix of the word the reader is typing.
  const [draft, setDraft] = useState(filters.q);
  useEffect(() => {
    setDraft(filters.q);
  }, [filters.q]);

  return (
    <SettingsCard>
      <SettingsRow label="Search" description="Every word has to appear in a record's title or body." align="start">
        <span className="flex items-center gap-2">
          <Input
            aria-label="Search memory"
            className="w-64"
            value={draft}
            disabled={busy}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                onApply({ q: draft });
              }
            }}
          />
          <ActionButton disabled={busy} onClick={() => onApply({ q: draft })}>
            Search
          </ActionButton>
        </span>
      </SettingsRow>
      <SettingsRow label="Type">
        <Select
          items={[
            { value: "", label: "Any type" },
            ...MEMORY_TYPES.map((type) => ({ value: type, label: typeLabel(type) })),
          ]}
          value={filters.type}
          onValueChange={(value) => onApply({ type: value as string })}
        >
          <SelectTrigger aria-label="Type" className="w-48" disabled={busy}>
            <SelectValue placeholder="Any type" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="">Any type</SelectItem>
            {MEMORY_TYPES.map((type) => (
              <SelectItem key={type} value={type}>
                {typeLabel(type)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </SettingsRow>
      <SettingsRow label="Status">
        <Select
          items={STATUS_CHOICES.map((choice) => ({ value: choice.value, label: choice.label }))}
          value={filters.status}
          onValueChange={(value) => onApply({ status: value as MemoryStatusChoice })}
        >
          <SelectTrigger aria-label="Status" className="w-48" disabled={busy}>
            <SelectValue placeholder="Waiting for approval" />
          </SelectTrigger>
          <SelectContent>
            {STATUS_CHOICES.map((choice) => (
              <SelectItem key={choice.value} value={choice.value}>
                {choice.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </SettingsRow>
      <SettingsRow label="Home" description="Where a record is filed.">
        <Select
          items={[
            { value: "", label: "Any home" },
            ...MEMORY_HOMES.map((home) => ({ value: home, label: homeLabel(home) })),
          ]}
          value={filters.home}
          onValueChange={(value) => onApply({ home: value as string })}
        >
          <SelectTrigger aria-label="Home" className="w-48" disabled={busy}>
            <SelectValue placeholder="Any home" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="">Any home</SelectItem>
            {MEMORY_HOMES.map((home) => (
              <SelectItem key={home} value={home}>
                {homeLabel(home)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </SettingsRow>
      <SettingsRow
        label="Agent"
        description="Narrows the loaded records to one agent — its profile, its expert tag or its writing. The memory seam has no per-agent query, so the count still describes the whole result."
      >
        <Select
          items={[
            { value: "", label: "Any agent" },
            ...directory.agents.map((agent) => ({ value: agent.id, label: agent.name })),
          ]}
          value={filters.agent}
          onValueChange={(value) => onApply({ agent: value as string })}
        >
          <SelectTrigger aria-label="Agent" className="w-48" disabled={busy}>
            <SelectValue placeholder={directory.agents.length === 0 ? "No agents read" : "Any agent"} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="">Any agent</SelectItem>
            {directory.agents.map((agent) => (
              <SelectItem key={agent.id} value={agent.id}>
                {agent.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </SettingsRow>
    </SettingsCard>
  );
}

/**
 * The memory plane: what the workspace remembers, and what is waiting to be
 * approved.
 *
 * The Service and Context-budget sections are S2-B's read-only view of the same
 * plane; they stay first because a reader who cannot reach moct-be needs to know
 * that before reading an empty list as "no memory".
 */
export function MoctMemoryTab({ connection }: { connection: MoctConnection }) {
  const canManage = connection.canManageMemory;
  const [status, reloadStatus] = useSeamRead(moctStatusPath(connection), parseMoctStatus);
  const [usage, reloadUsage] = useSeamRead(moctUsagePath(connection), parseMoctUsage);
  const directory = useMemoryDirectory(connection.workspaceId, connection.workspaceSlug, connection.apiBasePath);

  const [filters, setFilters] = useState<MemoryFilters>(MEMORY_DEFAULT_FILTERS);
  const [records, setRecords] = useState<MemoryRecord[]>([]);
  const [total, setTotal] = useState<number | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [list, setList] = useState<Load<null>>({ state: "loading" });
  const [failure, setFailure] = useState<string | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [reloadCount, setReloadCount] = useState(0);
  const [noteOpen, setNoteOpen] = useState(false);
  const [noteTitle, setNoteTitle] = useState("");
  const [noteBody, setNoteBody] = useState("");
  const [noteType, setNoteType] = useState("rule");
  const [noteHome, setNoteHome] = useState("");
  const [noteLevel, setNoteLevel] = useState("must");
  const [noteDecidedBy, setNoteDecidedBy] = useState("");
  const [noteSourceRef, setNoteSourceRef] = useState("");
  const [noteBusy, setNoteBusy] = useState(false);
  const [noteFailure, setNoteFailure] = useState<string | null>(null);
  const [noteNotice, setNoteNotice] = useState<string | null>(null);

  const path = useMemo(() => memoryListPath(filters, null, connection), [filters, connection]);

  /**
   * Read page one again. One effect for the whole first page keeps the filter
   * set, the request and the rendered page from drifting: changing any filter
   * rebuilds `path`, which is the only thing this depends on.
   */
  useEffect(() => {
    let cancelled = false;
    setList({ state: "loading" });
    setFailure(null);
    void callJson(path, parseMemoryPage).then((answer) => {
      if (cancelled) return;
      if (!answer.ok || answer.parsed === null) {
        setList({ state: "error", message: answer.message ?? "the server did not answer" });
        return;
      }
      setRecords(answer.parsed.records);
      setTotal(answer.parsed.total);
      setCursor(answer.parsed.nextCursor);
      setList({ state: "ready", value: null });
    });
    return () => {
      cancelled = true;
    };
  }, [path, reloadCount]);

  const loadMore = useCallback(async () => {
    if (cursor === null) return;
    setFailure(null);
    const answer = await callJson(memoryListPath(filters, cursor, connection), parseMemoryPage);
    if (!answer.ok || answer.parsed === null) {
      setFailure(answer.message ?? "the server did not answer");
      return;
    }
    // Appending must not double a record the previous page already carried: a
    // record updated between two reads can move pages under the cursor.
    setRecords((current) => {
      const seen = new Set(current.map((record) => record.id));
      return [...current, ...answer.parsed!.records.filter((record) => !seen.has(record.id))];
    });
    setTotal(answer.parsed.total);
    setCursor(answer.parsed.nextCursor);
  }, [cursor, filters, connection]);

  const shown = useMemo(() => visibleRecords(records, filters.agent), [records, filters.agent]);

  const openRecord = shown.find((record) => record.id === open) ?? records.find((record) => record.id === open);

  const reload = useCallback(() => {
    reloadStatus();
    reloadUsage();
    setReloadCount((current) => current + 1);
  }, [reloadStatus, reloadUsage]);

  const submitNote = useCallback(async () => {
    // The Owner files as themselves: moct-be saves a human write active, so
    // there is no gate to wait on. A credential in the text is moct-be's
    // 422, shown word for word.
    setNoteBusy(true);
    setNoteFailure(null);
    setNoteNotice(null);
    const trimmedHome = noteHome.trim();
    const homeKind =
      noteType === "decision" ? "task" : trimmedHome === "" ? "team" : "channel";
    const homeRef =
      homeKind === "team"
        ? ""
        : homeKind === "task"
          ? trimmedHome
          : trimmedHome === ""
            ? ""
            : `multica:project:${trimmedHome}`;
    const attrs: Record<string, unknown> =
      noteType === "rule"
        ? { level: noteLevel }
        : noteType === "decision"
          ? { decidedBy: noteDecidedBy.trim(), sourceRef: noteSourceRef.trim() }
          : {};
    const answer = await callJson(memoryCreatePath(connection), parseMemoryNote, {
      method: "POST",
      body: {
        type: noteType,
        title: noteTitle.trim(),
        body: noteBody,
        homeKind,
        homeRef,
        ...(Object.keys(attrs).length > 0 ? { attrs } : {}),
      } satisfies MemoryNoteInput,
    });
    setNoteBusy(false);
    if (!answer.ok || answer.parsed === null) {
      setNoteFailure(answer.message ?? "the server did not answer");
      return;
    }
    if (answer.parsed.held || answer.parsed.record === null) {
      setNoteFailure("moct-be held the note as a near-duplicate and wrote nothing. Change the title and save again.");
      return;
    }
    const created = answer.parsed.record;
    setRecords((current) => {
      if (current.some((record) => record.id === created.id)) return current;
      return [created, ...current];
    });
    setTotal((current) => (current === null ? null : current + 1));
    setNoteTitle("");
    setNoteBody("");
    setNoteDecidedBy("");
    setNoteSourceRef("");
    setNoteNotice("Saved. It is active now — a human note needs no approval.");
  }, [connection, noteType, noteTitle, noteBody, noteHome, noteLevel, noteDecidedBy, noteSourceRef]);

  /** One record's answer replaces the page's copy of it, so a row never lies. */
  const onDecided = useCallback((decided: MemoryRecord) => {
    setRecords((current) =>
      current.map((record) => (record.id === decided.id ? { ...record, ...decided } : record)),
    );
  }, []);

  /** A purge removes the record server-side, so the list drops its row. */
  const onPurged = useCallback((purgedId: string) => {
    setRecords((current) => current.filter((record) => record.id !== purgedId));
    setTotal((current) => (current === null ? null : Math.max(0, current - 1)));
  }, []);

  if (open !== null && openRecord !== undefined) {
    return (
      <RecordDetail
        id={open}
        summary={openRecord}
        directory={directory}
        connection={connection}
        canManage={canManage}
        onBack={() => setOpen(null)}
        onDecided={onDecided}
        onPurged={onPurged}
      />
    );
  }

  const health =
    status.state === "loading"
      ? "Checking…"
      : status.state === "error"
        ? status.message
        : !status.value.reachable
          ? "Unreachable"
          : status.value.ok
            ? "Reachable"
            : "Reachable, not healthy";

  return (
    <SettingsTab
      title="Memory"
      description="What this workspace's agents remember, where it lives, and what is still waiting for a human."
    >
      <SettingsSection title="Service" action={<RefreshButton onClick={reload} />}>
        <SettingsCard>
          <SettingsRow label="Status">
            <Status value={health} />
          </SettingsRow>
          <SettingsRow label="Schema">
            <Status
              value={
                status.state === "ready" && status.value.schema !== null
                  ? String(status.value.schema)
                  : "Not reported"
              }
            />
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title="Records"
        description={
          filters.status === "pending"
            ? "Waiting for approval. A worker's rule, decision or how_to_work lands here first and does not inject until it is approved; choose All to read the whole memory."
            : "Every status this workspace stores, filtered the way moct filters it."
        }
        action={<RefreshButton onClick={reload} />}
      >
        <Filters
          filters={filters}
          directory={directory}
          busy={list.state === "loading"}
          onApply={(next) => setFilters((current) => ({ ...current, ...next }))}
        />
        <SettingsCard>
          {list.state === "loading" ? (
            <SettingsRow label="Records">
              <Status value="Loading…" />
            </SettingsRow>
          ) : list.state === "error" ? (
            <SettingsRow label="Records">
              <span className="flex items-center gap-2">
                <Failure message={list.message} />
                <ActionButton label="Retry loading the records" onClick={reload}>
                  Retry
                </ActionButton>
              </span>
            </SettingsRow>
          ) : shown.length === 0 ? (
            <SettingsRow label="Records">
              <Status
                value={
                  records.length > 0
                    ? "No record on this page names that agent. Load more, or clear the agent filter."
                    : filters.status === "pending"
                      ? "Nothing is waiting for approval. Choose All to read the whole memory."
                      : "No record matches these filters."
                }
              />
            </SettingsRow>
          ) : (
            <ul data-memory-list className="divide-y divide-surface-border">
              {shown.map((record) => (
                <li key={record.id}>
                  <RecordRow
                    record={record}
                    directory={directory}
                    onOpen={() => setOpen(record.id)}
                  />
                </li>
              ))}
            </ul>
          )}
          <SettingsRow label="Count">
            <span className="flex items-center gap-2">
              {total === null ? null : (
                <Status
                  value={
                    shown.length === records.length
                      ? `${records.length} of ${total} records`
                      : `${shown.length} of the ${records.length} records loaded (${total} in total)`
                  }
                />
              )}
              {cursor === null ? null : (
                <ActionButton label="Load more records" onClick={() => void loadMore()}>
                  Load more
                </ActionButton>
              )}
            </span>
          </SettingsRow>
        </SettingsCard>
        {failure === null ? null : (
          <p role="alert" className="text-body text-destructive">
            {failure}
          </p>
        )}
      </SettingsSection>

      {canManage ? (
        <SettingsSection
          title="Add a note"
          description="Filed as you, active now — a human note needs no approval. A decision lives on an issue; a rule or how-to on the workspace or one project."
        >
          <SettingsCard>
            <SettingsRow label="Title">
              <Input
                aria-label="Note title"
                className="w-64"
                value={noteTitle}
                disabled={noteBusy}
                onChange={(event) => setNoteTitle(event.target.value)}
              />
            </SettingsRow>
            <SettingsRow label="Type">
              <Select
                items={[
                  { value: "rule", label: "Rule" },
                  { value: "decision", label: "Decision" },
                  { value: "how_to_work", label: "How to work" },
                ]}
                value={noteType}
                onValueChange={(value) => {
                  setNoteType(value as string);
                  setNoteOpen(false);
                  setNoteNotice(null);
                }}
              >
                <SelectTrigger aria-label="Note type" className="w-48" disabled={noteBusy}>
                  <SelectValue placeholder="Rule" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="rule">Rule</SelectItem>
                  <SelectItem value="decision">Decision</SelectItem>
                  <SelectItem value="how_to_work">How to work</SelectItem>
                </SelectContent>
              </Select>
            </SettingsRow>
            <SettingsRow label="Body">
              <Input
                aria-label="Note body"
                className="w-64"
                value={noteBody}
                disabled={noteBusy}
                onChange={(event) => setNoteBody(event.target.value)}
              />
            </SettingsRow>
            {noteType === "rule" ? (
              <SettingsRow label="Level" description="moct-be requires must or should on a rule.">
                <Select
                  items={[
                    { value: "must", label: "Must" },
                    { value: "should", label: "Should" },
                  ]}
                  value={noteLevel}
                  onValueChange={(value) => setNoteLevel(value as string)}
                >
                  <SelectTrigger aria-label="Note level" className="w-48" disabled={noteBusy}>
                    <SelectValue placeholder="Must" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="must">Must</SelectItem>
                    <SelectItem value="should">Should</SelectItem>
                  </SelectContent>
                </Select>
              </SettingsRow>
            ) : null}
            {noteType === "decision" ? (
              <SettingsRow
                label="Decided by"
                description="Who decided, in the Owner's own words — moct-be requires it."
              >
                <Input
                  aria-label="Note decided by"
                  className="w-64"
                  value={noteDecidedBy}
                  disabled={noteBusy}
                  onChange={(event) => setNoteDecidedBy(event.target.value)}
                />
              </SettingsRow>
            ) : null}
            {noteType === "decision" ? (
              <SettingsRow label="Source" description="Where the decision was taken — moct-be requires it.">
                <Input
                  aria-label="Note source ref"
                  className="w-64"
                  value={noteSourceRef}
                  disabled={noteBusy}
                  onChange={(event) => setNoteSourceRef(event.target.value)}
                />
              </SettingsRow>
            ) : null}
            {noteType === "decision" ? (
              <SettingsRow
                label="Issue home"
                description="The issue this decides, as multica:issue:<uuid>."
              >
                <Input
                  aria-label="Note issue home"
                  className="w-64"
                  placeholder="multica:issue:<uuid>"
                  value={noteHome}
                  disabled={noteBusy}
                  onChange={(event) => setNoteHome(event.target.value)}
                />
              </SettingsRow>
            ) : (
              <SettingsRow
                label="Project home"
                description="Empty files it on the workspace; picking a project files it there."
              >
                <Select
                  items={[
                    { value: "", label: "Workspace" },
                    ...directory.projects.map((project) => ({ value: project.id, label: project.name })),
                  ]}
                  value={noteHome}
                  onValueChange={(value) => setNoteHome(value as string)}
                >
                  <SelectTrigger aria-label="Note project home" className="w-48" disabled={noteBusy}>
                    <SelectValue placeholder="Workspace" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="">Workspace</SelectItem>
                    {directory.projects.map((project) => (
                      <SelectItem key={project.id} value={project.id}>
                        {project.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </SettingsRow>
            )}
            <SettingsRow label="Save">
              <span className="flex items-center gap-2">
                <ActionButton
                  label="Save note"
                  disabled={
                    noteBusy ||
                    noteTitle.trim() === "" ||
                    noteBody.trim() === "" ||
                    (noteType === "decision" &&
                      (noteHome.trim() === "" ||
                        noteDecidedBy.trim() === "" ||
                        noteSourceRef.trim() === ""))
                  }
                  onClick={() => setNoteOpen(true)}
                >
                  Save note
                </ActionButton>
              </span>
            </SettingsRow>
            {noteOpen ? (
              <SettingsRow label="Confirm">
                <span className="flex flex-wrap items-center gap-2">
                  <Status value={`Save “${noteTitle.trim() === "" ? "Untitled note" : noteTitle.trim()}” as ${noteType} active now?`} />
                  <ActionButton label="Confirm save note" disabled={noteBusy} onClick={() => void submitNote()}>
                    Confirm
                  </ActionButton>
                  <ActionButton label="Cancel save note" disabled={noteBusy} onClick={() => setNoteOpen(false)}>
                    Cancel
                  </ActionButton>
                </span>
              </SettingsRow>
            ) : null}
            {noteNotice === null ? null : (
              <SettingsRow label="Saved">
                <span role="status" className="text-body text-muted-foreground">
                  {noteNotice}
                </span>
              </SettingsRow>
            )}
            {noteFailure === null ? null : (
              <SettingsRow label="Not saved">
                <Failure message={noteFailure} />
              </SettingsRow>
            )}
          </SettingsCard>
        </SettingsSection>
      ) : null}

      <SettingsSection title="Context budget">
        <SettingsCard>
          <SettingsRow label="Cap per task">
            <Status
              value={
                usage.state === "ready" && usage.value.capTokens !== null
                  ? `${usage.value.capTokens} tokens`
                  : "Not reported"
              }
            />
          </SettingsRow>
          {usage.state === "error" ? (
            <SettingsRow label="Usage">
              <Status value={usage.message} />
            </SettingsRow>
          ) : null}
          {(usage.state === "ready" ? usage.value.homes : []).map((home) => (
            <SettingsRow
              key={home.home}
              label={home.home}
              description={
                home.blockedPending > 0
                  ? `${home.blockedPending} waiting for approval`
                  : undefined
              }
            >
              <Status value={`${home.usedTokens} tokens · ${home.records} records`} />
            </SettingsRow>
          ))}
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}
