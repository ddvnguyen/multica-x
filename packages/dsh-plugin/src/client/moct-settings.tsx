import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import {
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SettingsCard,
  SettingsRow,
  SettingsSection,
  SettingsTab,
  Switch,
  type ExtraSettingsGroup,
} from "../fork";
import { ISLAND_API_BASE } from "./config";

/**
 * The MOCT group in the settings page.
 *
 * S2-B puts the fork's real `SettingsPage` in this panel's sixth rail row. The
 * page is the web app's, so almost every group in it is about Multica itself;
 * this module adds the one group that only exists in dsh — the memory plane the
 * panel reaches through `/moct` (S2-A's closed seam) and the two policies that
 * shape how a run talks to a human.
 *
 * Three tabs, three planes:
 *
 * - Memory reads the seam (S2-B): the plane's health, its schema, and where the
 *   always-load context budget is spent. Read-only: the seam exposes memory
 *   verbs (approve, retract), but a settings page is the wrong place to decide
 *   a record.
 * - Connection (S2-C) says how this panel is wired and whether a run gets the
 *   pre-run memory block at all. The switch is the panel's first write, so it
 *   is surfaced as the server's, not the panel's: the answer to a write is the
 *   body the server would answer a read with, and `editable` comes from the
 *   server's view of the reader's role.
 * - Interactions (S2-C) holds the ask/plan wait: a workspace default (ten
 *   minutes) with a per-project override. Stored and surfaced only — nothing
 *   here decides when a parked run gives up, which is M-131's job.
 */

/** Pages this panel does not render, hidden from nav, selector and search. */
export const HIDDEN_SETTINGS_TABS = [
  // Billing is a web-app subscription surface; the panel has no account chrome.
  "billing",
  // GitHub hosting and repository linking are integration setup, Q8-hidden.
  "code",
  // Slack/Lark/DingTalk/WeCom/Telegram delivery belongs to the web app.
  "channels",
  // Plugins install into the web app's runtime, not into this shell.
  "plugins",
  // Composio apps are per-account OAuth; the island never opens a new origin.
  "apps",
  // The island swallows key events before the fork sees them, so the shortcut
  // reference would document bindings that do not reach it here.
  "shortcuts",
] as const;

/** What the host's `/moct/status` says about the memory plane. */
export interface MoctStatus {
  ok: boolean;
  schema: number | null;
  reachable: boolean;
}

/** One home's use of the always-load cap, from `/moct/team/v1/memory/usage`. */
export interface MoctHomeUsage {
  home: string;
  usedTokens: number;
  records: number;
  blockedPending: number;
}

export interface MoctUsage {
  capTokens: number | null;
  homes: MoctHomeUsage[];
}

/** How this panel is wired, as the host half reported it at boot. */
export interface MoctConnection {
  /** Workspace the panel speaks as; empty means the first readable one. */
  workspaceSlug: string;
  /** Workspace UUID the settings writes address. Empty when unresolved. */
  workspaceId: string;
  /** Same-origin realtime path. */
  wsPath: string;
  /** Why the host cannot proxy, when it cannot. Never a credential. */
  problem: string | null;
}

export const MOCT_STATUS_PATH = "/moct/status";
export const MOCT_USAGE_PATH = "/moct/team/v1/memory/usage";

/** The ask/plan wait the server ships: ten minutes (M-123). */
export const MOCT_DEFAULT_TIMEOUT_SECONDS = 600;

/** One project's ask/plan wait. */
export interface MoctProjectOverride {
  projectId: string;
  projectTitle: string;
  timeoutSeconds: number;
}

/** The Interactions tab's server state. */
export interface MoctInteractions {
  timeoutSeconds: number;
  /** False while the workspace still runs on the built-in ten minutes. */
  timeoutSecondsSet: boolean;
  minTimeoutSeconds: number;
  maxTimeoutSeconds: number;
  /** Whether the reader may write; the server decides, not the panel. */
  editable: boolean;
  overrides: MoctProjectOverride[];
}

/** The Connection tab's server state. */
export interface MoctMemorySettings {
  memoryInject: boolean;
  /** False while the switch sits at its built-in default (on). */
  memoryInjectSet: boolean;
  editable: boolean;
}

/** A project the reader can give its own wait. */
export interface MoctProjectOption {
  id: string;
  title: string;
}

/**
 * The workspace-scoped API the two new tabs use.
 *
 * Same-origin paths under the dsh host's `/multica` transport, like the unread
 * summary and the board: the host attaches the token, so the browser never
 * holds a Multica credential and no request is CORS-shaped.
 */
export function moctApiPaths(workspaceId: string) {
  const workspace = `${ISLAND_API_BASE}/api/workspaces/${encodeURIComponent(workspaceId)}`;
  return {
    interactions: `${workspace}/interactions`,
    memory: `${workspace}/moct`,
    projects: `${ISLAND_API_BASE}/api/projects`,
  };
}

function asRecord(value: unknown): Record<string, unknown> {
  return typeof value === "object" && value !== null
    ? (value as Record<string, unknown>)
    : {};
}

function wholeNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) ? value : null;
}

/** A status answer, with the defaults a missing field deserves. */
export function parseMoctStatus(body: unknown): MoctStatus {
  const record = asRecord(body);
  return {
    ok: record["ok"] === true,
    schema: wholeNumber(record["schema"]),
    reachable: record["reachable"] === true,
  };
}

/**
 * A usage answer.
 *
 * moct-be's `homes` is the part a reader acts on: it is where the always-load
 * cap is actually spent. An entry without a home name is not one this page can
 * label, so it is dropped rather than rendered as an empty row.
 */
export function parseMoctUsage(body: unknown): MoctUsage {
  const record = asRecord(body);
  const homes = Array.isArray(record["homes"])
    ? record["homes"].flatMap((entry) => {
        const home = asRecord(entry);
        const name = home["home"];
        if (typeof name !== "string" || name === "") return [];
        return [
          {
            home: name,
            usedTokens: wholeNumber(home["usedTokens"]) ?? 0,
            records: wholeNumber(home["records"]) ?? 0,
            blockedPending: wholeNumber(home["blockedPending"]) ?? 0,
          },
        ];
      })
    : [];
  return { capTokens: wholeNumber(record["capTokens"]), homes };
}

/**
 * An interactions answer.
 *
 * The bounds come from the server and are only defaulted when the answer cannot
 * carry them; a stored value that is missing or out of range reads as the
 * built-in default so a malformed body cannot put a nonsense number in an
 * editor the reader is about to submit.
 */
export function parseMoctInteractions(body: unknown): MoctInteractions {
  const record = asRecord(body);
  const min = wholeNumber(record["min_timeout_seconds"]) ?? 60;
  const max = wholeNumber(record["max_timeout_seconds"]) ?? 86400;
  const stored = wholeNumber(record["timeout_seconds"]);
  const timeoutSeconds =
    stored !== null && stored >= min && stored <= max ? stored : MOCT_DEFAULT_TIMEOUT_SECONDS;
  const overrides = Array.isArray(record["overrides"])
    ? record["overrides"].flatMap((entry) => {
        const override = asRecord(entry);
        const projectId = override["project_id"];
        const seconds = wholeNumber(override["timeout_seconds"]);
        if (typeof projectId !== "string" || projectId === "" || seconds === null) return [];
        const projectTitle = override["project_title"];
        return [
          {
            projectId,
            projectTitle: typeof projectTitle === "string" && projectTitle !== "" ? projectTitle : projectId,
            timeoutSeconds: seconds,
          },
        ];
      })
    : [];
  return {
    timeoutSeconds,
    timeoutSecondsSet: record["timeout_seconds_set"] === true,
    minTimeoutSeconds: min,
    maxTimeoutSeconds: max,
    editable: record["editable"] === true,
    overrides,
  };
}

/** A memory-switch answer. An absent value reads as on — the server's default. */
export function parseMoctMemorySettings(body: unknown): MoctMemorySettings {
  const record = asRecord(body);
  return {
    memoryInject: record["memory_inject"] !== false,
    memoryInjectSet: record["memory_inject_set"] === true,
    editable: record["editable"] === true,
  };
}

/** The projects a reader may override, newest-tolerant and title-sorted. */
export function parseMoctProjectOptions(body: unknown): MoctProjectOption[] {
  const list = Array.isArray(body) ? body : asRecord(body)["projects"];
  if (!Array.isArray(list)) return [];
  const options = list.flatMap((entry) => {
    const project = asRecord(entry);
    const id = project["id"];
    if (typeof id !== "string" || id === "") return [];
    const title = project["title"];
    return [{ id, title: typeof title === "string" && title !== "" ? title : id }];
  });
  return options.sort((a, b) => a.title.localeCompare(b.title));
}

type Load<T> =
  | { state: "loading" }
  | { state: "ready"; value: T }
  | { state: "error"; message: string };

interface Answer<T> {
  ok: boolean;
  status: number;
  parsed: T | null;
  message: string | null;
}

/** One JSON request, with the server's own error text when it refuses. */
async function callJson<T>(
  path: string,
  parse: (body: unknown) => T,
  init: { method: "GET" | "PUT"; body?: unknown } = { method: "GET" },
): Promise<Answer<T>> {
  try {
    const response = await fetch(path, {
      method: init.method,
      headers:
        init.body === undefined
          ? { accept: "application/json" }
          : { accept: "application/json", "content-type": "application/json" },
      credentials: "same-origin",
      ...(init.body === undefined ? {} : { body: JSON.stringify(init.body) }),
    });
    const body: unknown = await response.json().catch(() => null);
    if (!response.ok) {
      const reason = asRecord(body)["error"];
      return {
        ok: false,
        status: response.status,
        parsed: null,
        message:
          typeof reason === "string" && reason !== "" ? reason : `responded ${response.status}`,
      };
    }
    return { ok: true, status: response.status, parsed: parse(body), message: null };
  } catch (cause) {
    return {
      ok: false,
      status: 0,
      parsed: null,
      message: cause instanceof Error ? cause.message : String(cause),
    };
  }
}

/**
 * One read from the seam, with an explicit error state.
 *
 * A failure is shown, never defaulted: "unreachable" and "no records" are
 * different answers, and a settings page that quietly renders zeros for a dead
 * service is worse than one that says it is down.
 */
function useSeamRead<T>(path: string, parse: (body: unknown) => T) {
  const [attempt, setAttempt] = useState(0);
  const [load, setLoad] = useState<Load<T>>({ state: "loading" });

  useEffect(() => {
    let cancelled = false;
    setLoad({ state: "loading" });
    fetch(path, {
      headers: { accept: "application/json" },
      credentials: "same-origin",
    })
      .then(async (response) => {
        const body: unknown = await response.json().catch(() => null);
        if (cancelled) return;
        if (!response.ok) {
          const reason = asRecord(body)["error"];
          setLoad({
            state: "error",
            message:
              typeof reason === "string" && reason !== ""
                ? reason
                : `responded ${response.status}`,
          });
          return;
        }
        setLoad({ state: "ready", value: parse(body) });
      })
      .catch((cause: unknown) => {
        if (cancelled) return;
        setLoad({
          state: "error",
          message: cause instanceof Error ? cause.message : String(cause),
        });
      });
    return () => {
      cancelled = true;
    };
  }, [path, parse, attempt]);

  return [load, () => setAttempt((current) => current + 1)] as const;
}

function RefreshButton({ onClick }: { onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="inline-flex h-8 items-center rounded-lg border border-input px-2.5 text-label font-medium text-foreground transition-colors hover:bg-surface-hover focus-visible:outline-2 focus-visible:outline-ring"
    >
      Refresh
    </button>
  );
}

function ActionButton({
  children,
  label,
  onClick,
  disabled = false,
}: {
  children: ReactNode;
  label?: string;
  onClick: () => void;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      onClick={onClick}
      disabled={disabled}
      className="inline-flex h-8 items-center rounded-lg border border-input px-2.5 text-label font-medium text-foreground transition-colors hover:bg-surface-hover focus-visible:outline-2 focus-visible:outline-ring disabled:cursor-not-allowed disabled:opacity-50"
    >
      {children}
    </button>
  );
}

function Status({ value }: { value: string }) {
  return <span className="text-body text-muted-foreground">{value}</span>;
}

function Failure({ message }: { message: string }) {
  return (
    <span role="alert" className="text-body text-destructive">
      {message}
    </span>
  );
}

/** Minutes as the editor shows them: whole when they are, otherwise one decimal. */
export function minutesValue(seconds: number): string {
  const minutes = seconds / 60;
  return Number.isInteger(minutes) ? String(minutes) : String(Math.round(minutes * 10) / 10);
}

/** Minutes as the API takes them, or null when the input is not a usable number. */
export function secondsFromMinutes(input: string): number | null {
  const minutes = Number.parseFloat(input.trim());
  if (!Number.isFinite(minutes) || minutes <= 0) return null;
  return Math.round(minutes * 60);
}

/**
 * The memory plane: reachability, schema, and where the budget is spent.
 */
export function MoctMemoryTab() {
  const [status, reloadStatus] = useSeamRead(MOCT_STATUS_PATH, parseMoctStatus);
  const [usage, reloadUsage] = useSeamRead(MOCT_USAGE_PATH, parseMoctUsage);

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
    <SettingsTab title="Memory">
      <SettingsSection
        title="Service"
        action={
          <RefreshButton
            onClick={() => {
              reloadStatus();
              reloadUsage();
            }}
          />
        }
      >
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
              <Status
                value={`${home.usedTokens} tokens · ${home.records} records`}
              />
            </SettingsRow>
          ))}
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}

/**
 * How this panel is wired, whether the memory plane answers, and whether a run
 * gets memory.
 *
 * The switch is the panel's first write. It is rendered from the server's
 * answer — including `editable`, which the server derives from the reader's
 * workspace role — so a member sees the state and the owner edits it, with no
 * second source of truth in the browser.
 */
export function MoctConnectionTab({ connection }: { connection: MoctConnection }) {
  const [status, reloadStatus] = useSeamRead(MOCT_STATUS_PATH, parseMoctStatus);
  const paths = useMemo(() => moctApiPaths(connection.workspaceId), [connection.workspaceId]);
  const [settings, setSettings] = useState<Load<MoctMemorySettings>>({ state: "loading" });
  const [failure, setFailure] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (connection.workspaceId === "") {
      setSettings({ state: "error", message: "the workspace is not resolved yet" });
      return;
    }
    let cancelled = false;
    setSettings({ state: "loading" });
    void callJson(paths.memory, parseMoctMemorySettings).then((answer) => {
      if (cancelled) return;
      setSettings(
        answer.ok && answer.parsed !== null
          ? { state: "ready", value: answer.parsed }
          : { state: "error", message: answer.message ?? "the server did not answer" },
      );
    });
    return () => {
      cancelled = true;
    };
  }, [paths.memory, connection.workspaceId, attempt]);

  const current = settings.state === "ready" ? settings.value : null;

  const toggle = useCallback(
    async (next: boolean) => {
      setSaving(true);
      setFailure(null);
      const answer = await callJson(paths.memory, parseMoctMemorySettings, {
        method: "PUT",
        body: { memory_inject: next },
      });
      setSaving(false);
      if (!answer.ok || answer.parsed === null) {
        // The server is the authority on both the value and the role: a refusal
        // leaves the switch showing what is stored, not what was clicked.
        setFailure(answer.message ?? "the server did not answer");
        return;
      }
      setSettings({ state: "ready", value: answer.parsed });
    },
    [paths.memory],
  );

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

  const injectLabel =
    current === null
      ? "Not reported"
      : current.memoryInject
        ? current.memoryInjectSet
          ? "On"
          : "On (default)"
        : "Off";

  return (
    <SettingsTab title="Connection">
      <SettingsSection title="Multica">
        <SettingsCard>
          <SettingsRow label="Workspace">
            <Status
              value={
                connection.workspaceSlug === ""
                  ? "First readable workspace"
                  : connection.workspaceSlug
              }
            />
          </SettingsRow>
          <SettingsRow label="Realtime channel">
            <Status value={connection.wsPath} />
          </SettingsRow>
          {connection.problem === null ? null : (
            <SettingsRow label="Unavailable">
              <Status value={connection.problem} />
            </SettingsRow>
          )}
        </SettingsCard>
      </SettingsSection>
      <SettingsSection
        title="Memory plane"
        action={<RefreshButton onClick={reloadStatus} />}
      >
        <SettingsCard>
          <SettingsRow label="moct-be">
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
      <SettingsSection title="Pre-run memory">
        <SettingsCard>
          <SettingsRow
            label="Inject memory into new runs"
            description="Relevant records are added to the task prompt. The memory tools stay available either way."
          >
            <span className="flex items-center gap-2">
              {settings.state === "loading" ? (
                <Status value="Loading…" />
              ) : settings.state === "error" ? (
                <Status value={injectLabel} />
              ) : (
                <Switch
                  aria-label="Inject memory into new runs"
                  checked={current?.memoryInject ?? true}
                  disabled={saving || !current?.editable}
                  onCheckedChange={(checked) => {
                    void toggle(checked);
                  }}
                />
              )}
              <Status value={injectLabel} />
            </span>
          </SettingsRow>
          {settings.state === "error" ? (
            <SettingsRow label="Settings">
              <span className="flex items-center gap-2">
                <Status value={settings.message} />
                <ActionButton
                  label="Retry loading the memory switch"
                  onClick={() => setAttempt((value) => value + 1)}
                >
                  Retry
                </ActionButton>
              </span>
            </SettingsRow>
          ) : null}
          {current !== null && !current.editable ? (
            <SettingsRow label="Changes">
              <Status value="Only a workspace owner or admin can change this" />
            </SettingsRow>
          ) : null}
          {failure === null ? null : (
            <SettingsRow label="Last change">
              <Failure message={failure} />
            </SettingsRow>
          )}
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}

/** One editable timeout: minutes in, seconds out, with the server's answer back. */
function TimeoutEditor({
  label,
  seconds,
  editable,
  saving,
  onSave,
  onClear,
  clearLabel,
}: {
  label: string;
  seconds: number;
  editable: boolean;
  saving: boolean;
  onSave: (seconds: number) => void;
  onClear?: () => void;
  clearLabel?: string;
}) {
  const [draft, setDraft] = useState(() => minutesValue(seconds));

  // A write (or another reader's) replaces the value underneath the editor;
  // the row must then show what is stored, not a stale draft.
  useEffect(() => {
    setDraft(minutesValue(seconds));
  }, [seconds]);

  const submit = () => {
    const parsed = secondsFromMinutes(draft);
    if (parsed === null) return;
    onSave(parsed);
  };

  if (!editable) {
    return <Status value={`${minutesValue(seconds)} minutes`} />;
  }

  return (
    <span className="flex items-center gap-2">
      <Input
        aria-label={label}
        className="w-20"
        inputMode="numeric"
        value={draft}
        disabled={saving}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            submit();
          }
        }}
      />
      <Status value="min" />
      <ActionButton label={`Save ${label}`} onClick={submit} disabled={saving}>
        Save
      </ActionButton>
      {onClear === undefined ? null : (
        <ActionButton
          label={clearLabel ?? `Clear ${label}`}
          onClick={onClear}
          disabled={saving}
        >
          Clear
        </ActionButton>
      )}
    </span>
  );
}

/**
 * The ask/plan wait: the workspace default and the projects that override it.
 *
 * Nothing here enforces the value. A parked run is not cancelled and not
 * resumed by this page; the timeout is stored and surfaced so M-131 has one
 * place to read it from.
 */
export function MoctInteractionsTab({ connection }: { connection: MoctConnection }) {
  const paths = useMemo(() => moctApiPaths(connection.workspaceId), [connection.workspaceId]);
  const [interactions, setInteractions] = useState<Load<MoctInteractions>>({ state: "loading" });
  const [projects, setProjects] = useState<MoctProjectOption[]>([]);
  const [selected, setSelected] = useState("");
  const [failure, setFailure] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const reload = useCallback(async () => {
    if (connection.workspaceId === "") {
      setInteractions({ state: "error", message: "the workspace is not resolved yet" });
      return;
    }
    setInteractions({ state: "loading" });
    const answer = await callJson(paths.interactions, parseMoctInteractions);
    setInteractions(
      answer.ok && answer.parsed !== null
        ? { state: "ready", value: answer.parsed }
        : { state: "error", message: answer.message ?? "the server did not answer" },
    );
  }, [paths.interactions, connection.workspaceId]);

  useEffect(() => {
    void reload();
  }, [reload]);

  // The picker reads the workspace's projects lazily, and a failure is not a
  // blocker: the overrides that exist are still listed and editable.
  useEffect(() => {
    if (connection.workspaceId === "") return;
    let cancelled = false;
    void callJson(paths.projects, parseMoctProjectOptions).then((answer) => {
      if (cancelled || !answer.ok || answer.parsed === null) return;
      setProjects(answer.parsed);
    });
    return () => {
      cancelled = true;
    };
  }, [paths.projects, connection.workspaceId]);

  const current = interactions.state === "ready" ? interactions.value : null;

  const saveWorkspace = useCallback(
    async (seconds: number) => {
      setSaving(true);
      setFailure(null);
      const answer = await callJson(paths.interactions, parseMoctInteractions, {
        method: "PUT",
        body: { timeout_seconds: seconds },
      });
      setSaving(false);
      if (!answer.ok || answer.parsed === null) {
        setFailure(answer.message ?? "the server did not answer");
        return;
      }
      setInteractions({ state: "ready", value: answer.parsed });
    },
    [paths.interactions],
  );

  const saveProject = useCallback(
    async (projectId: string, seconds: number | null) => {
      setSaving(true);
      setFailure(null);
      const answer = await callJson(
        `${paths.projects}/${encodeURIComponent(projectId)}/interactions`,
        parseMoctInteractions,
        { method: "PUT", body: { timeout_seconds: seconds } },
      );
      setSaving(false);
      if (!answer.ok) {
        setFailure(answer.message ?? "the server did not answer");
        return;
      }
      setSelected("");
      await reload();
    },
    [paths.projects, reload],
  );

  const available = projects.filter(
    (project) => !(current?.overrides ?? []).some((override) => override.projectId === project.id),
  );
  const canEdit = current?.editable === true;

  return (
    <SettingsTab
      title="Interactions"
      description="How long an ask or a plan waits for a human answer."
    >
      <SettingsSection
        title="Ask and plan"
        action={<RefreshButton onClick={() => void reload()} />}
      >
        <SettingsCard>
          {interactions.state === "loading" ? (
            <SettingsRow label="Timeout">
              <Status value="Loading…" />
            </SettingsRow>
          ) : interactions.state === "error" ? (
            <SettingsRow label="Timeout">
              <span className="flex items-center gap-2">
                <Failure message={interactions.message} />
                <ActionButton label="Retry loading the ask/plan timeout" onClick={() => void reload()}>
                  Retry
                </ActionButton>
              </span>
            </SettingsRow>
          ) : (
            <SettingsRow
              label="Workspace timeout"
              description={
                current?.timeoutSecondsSet === false
                  ? `${MOCT_DEFAULT_TIMEOUT_SECONDS / 60} minutes is the default`
                  : undefined
              }
            >
              <TimeoutEditor
                label="Workspace ask/plan timeout in minutes"
                seconds={current?.timeoutSeconds ?? MOCT_DEFAULT_TIMEOUT_SECONDS}
                editable={canEdit}
                saving={saving}
                onSave={(seconds) => void saveWorkspace(seconds)}
              />
            </SettingsRow>
          )}
          {current !== null && !current.editable ? (
            <SettingsRow label="Changes">
              <Status value="Only a workspace owner or admin can change this" />
            </SettingsRow>
          ) : null}
          {failure === null ? null : (
            <SettingsRow label="Last change">
              <Failure message={failure} />
            </SettingsRow>
          )}
        </SettingsCard>
      </SettingsSection>
      <SettingsSection title="Project overrides">
        <SettingsCard>
          {(current?.overrides ?? []).length === 0 ? (
            <SettingsRow label="Overrides">
              <Status value="Every project uses the workspace timeout" />
            </SettingsRow>
          ) : (
            (current?.overrides ?? []).map((override) => (
              <SettingsRow key={override.projectId} label={override.projectTitle}>
                <TimeoutEditor
                  label={`${override.projectTitle} ask/plan timeout in minutes`}
                  seconds={override.timeoutSeconds}
                  editable={canEdit}
                  saving={saving}
                  onSave={(seconds) => void saveProject(override.projectId, seconds)}
                  onClear={() => void saveProject(override.projectId, null)}
                  clearLabel={`Clear the ${override.projectTitle} override`}
                />
              </SettingsRow>
            ))
          )}
          {canEdit && available.length > 0 ? (
            <SettingsRow label="Add an override">
              <span className="flex items-center gap-2">
                <Select
                  items={available.map((project) => ({
                    value: project.id,
                    label: project.title,
                  }))}
                  value={selected}
                  onValueChange={(value) => setSelected(value as string)}
                >
                  <SelectTrigger
                    aria-label="Project"
                    className="w-56"
                    disabled={saving}
                  >
                    <SelectValue placeholder="Choose a project" />
                  </SelectTrigger>
                  <SelectContent>
                    {available.map((project) => (
                      <SelectItem key={project.id} value={project.id}>
                        {project.title}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <ActionButton
                  label="Add the project override"
                  disabled={saving || selected === ""}
                  onClick={() =>
                    void saveProject(selected, current?.timeoutSeconds ?? MOCT_DEFAULT_TIMEOUT_SECONDS)
                  }
                >
                  Add
                </ActionButton>
              </span>
            </SettingsRow>
          ) : null}
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}

function layersGeometry(): ReactNode[] {
  return [
    <path key="a" d="M12 3 3 7.5 12 12l9-4.5Z" />,
    <path key="b" d="M3 12.5 12 17l9-4.5" />,
    <path key="c" d="M3 17 12 21.5 21 17" />,
  ];
}

function mark(className: string | undefined, children: ReactNode[]) {
  return (
    <svg
      {...(className === undefined ? {} : { className })}
      width={16}
      height={16}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.6}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {children}
    </svg>
  );
}

/** The group's mark in the second sidebar. */
export function MoctMark({ className }: { className?: string }) {
  return mark(className, layersGeometry());
}

function MemoryIcon({ className }: { className?: string }) {
  return mark(className, layersGeometry());
}

function ConnectionIcon({ className }: { className?: string }) {
  return mark(className, [
    <path key="a" d="M9 3v5M15 3v5" />,
    <path key="b" d="M6 8h12v3a6 6 0 0 1-12 0Z" />,
    <path key="c" d="M12 17v4" />,
  ]);
}

function InteractionsIcon({ className }: { className?: string }) {
  return mark(className, [
    <path key="a" d="M4 5h16v10H8l-4 4Z" />,
    <path key="b" d="M9 9h6M9 12h4" />,
  ]);
}

/**
 * The MOCT group, as `SettingsPage` wants it.
 *
 * Built once per connection: each tab reads its own source on mount, so nothing
 * here touches the network until a reader opens it.
 */
export function createMoctGroup(connection: MoctConnection): ExtraSettingsGroup {
  return {
    key: "moct",
    label: "MOCT",
    mark: <MoctMark />,
    entries: [
      {
        value: "moct-memory",
        label: "Memory",
        icon: MemoryIcon,
        content: <MoctMemoryTab />,
      },
      {
        value: "moct-connection",
        label: "Connection",
        icon: ConnectionIcon,
        content: <MoctConnectionTab connection={connection} />,
      },
      {
        value: "moct-interactions",
        label: "Interactions",
        icon: InteractionsIcon,
        content: <MoctInteractionsTab connection={connection} />,
      },
    ],
  };
}
