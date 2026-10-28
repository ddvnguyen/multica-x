import { useCallback, useEffect, useMemo, useState } from "react";
import { SettingsCard, SettingsRow, SettingsSection } from "../fork";
import type { ExtraAgentSection } from "../fork";
import {
  ActionButton,
  callJson,
  Failure,
  MOCT_USAGE_PATH,
  parseMoctUsage,
  RefreshButton,
  Status,
  useSeamRead,
  type MoctConnection,
  type Answer,
} from "./moct-seam";
import {
  agentRef,
  homeLabel,
  homeName,
  memoryDecisionPath,
  memoryListPath,
  parseMemoryPage,
  parseMemoryRecord,
  recordNamesAgent,
  statusLabel,
  typeLabel,
  useMemoryDirectory,
  type MemoryRecord,
  type MemoryPage,
} from "./moct-memory";

/**
 * The agent page's Memory section (S2-E): one agent's records, pending first.
 *
 * The workspace memory overview (`./moct-memory`, S2-D) is the whole plane;
 * this section is one agent's slice of it, read the same way and through the
 * same seam. `agentRef` and `recordNamesAgent` do the narrowing the seam has no
 * query for, the two verbs are still moct-be's `approve` and `retract`, and
 * every list is the page moct answered — never a guessed one.
 *
 * One agent, three answers: the pending queue first, because that is why a
 * reader is on the page; the agent's own records grouped by type; and the
 * seam's always-load cap beside what the loaded page spends.
 */

/** How many list pages one read may pull before it stops. */
const MAX_LOAD_PAGES = 25;

/**
 * The pending read: `status=pending` with **no `homes` filter**, so a rule that
 * merely tags the agent from a task or the team still surfaces. Client-narrowed
 * to this agent's ref before render — nothing of another agent's may pass, not
 * even transiently, and the count anything claims is the whole result's.
 */
export function pendingListPath(cursor: string | null): string {
  return memoryListPath({ q: "", type: "", status: "pending", home: "", agent: "" }, cursor);
}

/** The records read: `status=all` on expert homes, narrowed to the agent. */
export function agentListPath(cursor: string | null): string {
  return memoryListPath({ q: "", type: "", status: "all", home: "expert", agent: "" }, cursor);
}

function mergeRecords(
  current: readonly MemoryRecord[],
  added: readonly MemoryRecord[],
): MemoryRecord[] {
  const seen = new Set(current.map((record) => record.id));
  return [...current, ...added.filter((record) => !seen.has(record.id))];
}

type ReadState = "loading" | "ready" | "error";

/** A refusal's reason, or null when the answer is one the page can use. */
function refusal(answer: { ok: boolean; message: string | null }): string | null {
  return answer.ok ? null : answer.message ?? "the server did not answer";
}

function Chip({ label, value }: { label?: string; value: string }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 text-caption text-muted-foreground">
      {label === undefined ? null : <span className="opacity-70">{label}</span>}
      <span>{value}</span>
    </span>
  );
}

/**
 * One page-bounded read: page one, then cursor-load more by hand — except with
 * `autoPage`, which drains the whole cursor chain up front (what a queue that
 * must not silently drop a page below the fold has to do).
 */
function useMemoryPages(
  listPath: (cursor: string | null) => string,
  autoPage: boolean,
  enabled: boolean,
  reloadKey: number,
) {
  const [records, setRecords] = useState<MemoryRecord[]>([]);
  const [total, setTotal] = useState<number | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [state, setState] = useState<ReadState>("loading");
  const [failure, setFailure] = useState<string | null>(null);
  const [moving, setMoving] = useState(false);

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    setRecords([]);
    setCursor(null);
    setTotal(null);
    setFailure(null);
    setState("loading");

    void (async () => {
      let next: string | null = null;
      for (let page = 0; page < MAX_LOAD_PAGES; page++) {
        const answer: Answer<MemoryPage> = await callJson(listPath(next), parseMemoryPage);
        if (cancelled) return;
        const refused = refusal(answer);
        if (refused !== null || answer.parsed === null) {
          setState("error");
          setFailure(refused ?? "the server did not answer");
          return;
        }
        setRecords((current) => mergeRecords(current, answer.parsed!.records));
        setTotal(answer.parsed.total);
        next = answer.parsed.nextCursor;
        if (next === null || !autoPage) break;
      }
      if (cancelled) return;
      setCursor(next);
      setState("ready");
    })();

    return () => {
      cancelled = true;
    };
  }, [listPath, autoPage, enabled, reloadKey]);

  const loadMore = useCallback(async () => {
    if (cursor === null || moving) return;
    setMoving(true);
    setFailure(null);
    try {
      const answer: Answer<MemoryPage> = await callJson(listPath(cursor), parseMemoryPage);
      const refused = refusal(answer);
      if (refused !== null || answer.parsed === null) {
        setFailure(refused ?? "the server did not answer");
        return;
      }
      setRecords((current) => mergeRecords(current, answer.parsed!.records));
      setTotal(answer.parsed.total);
      setCursor(answer.parsed.nextCursor);
    } finally {
      setMoving(false);
    }
  }, [cursor, moving, listPath]);

  /**
   * One decision's answer replaces the local copies, so no row on the page
   * still claims the old status or version.
   */
  const replaceRecord = useCallback((decided: MemoryRecord) => {
    setRecords((current) =>
      current.map((record) => (record.id === decided.id ? decided : record)),
    );
  }, []);

  return { records, total, cursor, state, failure, loadMore, moving, replaceRecord };
}

type Directory = ReturnType<typeof useMemoryDirectory>;

function HomeChip({ home, directory }: { home: MemoryRecord["home"]; directory: Directory }) {
  return (
    <Chip label="Home" value={`${homeLabel(home.kind)} · ${homeName(home, directory)}`} />
  );
}

function SpendChip({ record }: { record: MemoryRecord }) {
  return (
    <Chip
      label="Always-load"
      value={
        record.flag === "blocked_budget"
          ? `${record.tokens} tokens · held: its home's always-load set is full`
          : `${record.tokens} tokens`
      }
    />
  );
}

/**
 * One pending record the reader can decide on.
 *
 * The seam's `retract` is what a reader calls reject — the decided wording of
 * S2-D — and the version sent is the one shown, so a record that moved
 * underneath is moct's 409 and a reload is what fixes it.
 */
function PendingRow({
  record,
  busy,
  onApprove,
  onReject,
}: {
  record: MemoryRecord;
  busy: boolean;
  onApprove: () => void;
  onReject: () => void;
}) {
  const title = record.title ?? "Untitled record";
  return (
    <SettingsRow
      anchor={record.id}
      align="start"
      label={
        <span data-agent-pending={record.id} className="text-body font-medium">
          {title}
        </span>
      }
      description={
        <span className="flex flex-wrap items-center gap-1.5">
          <Chip label="Type" value={typeLabel(record.type)} />
          <Chip value={statusLabel(record.status)} />
          <Status value={record.updatedAt} />
        </span>
      }
    >
      <span className="flex items-center gap-2">
        <ActionButton label={`Approve ${title}`} disabled={busy} onClick={onApprove}>
          <span data-memory-action="approve">Approve</span>
        </ActionButton>
        <ActionButton label={`Reject ${title}`} disabled={busy} onClick={onReject}>
          <span data-memory-action="retract">Reject</span>
        </ActionButton>
      </span>
    </SettingsRow>
  );
}

/** One record as a ledger row: what it is, where it lives, its spend. */
function RecordRow({ record, directory }: { record: MemoryRecord; directory: Directory }) {
  const title = record.title ?? "Untitled record";
  return (
    <SettingsRow
      anchor={record.id}
      align="start"
      label={
        <span data-agent-record={record.id} className="text-body font-medium">
          {title}
        </span>
      }
      description={
        <span className="flex flex-wrap items-center gap-1.5">
          <Chip label="Type" value={typeLabel(record.type)} />
          <Chip value={statusLabel(record.status)} />
          <HomeChip home={record.home} directory={directory} />
          <SpendChip record={record} />
          <Status value={`v${record.version} · ${record.updatedAt}`} />
        </span>
      }
    >
      <Status value="Loaded" />
    </SettingsRow>
  );
}

/** moct's records vocabulary, for group ordering; unknown types come last. */
const ORDERED_TYPES = ["rule", "decision", "convention", "how_to_work", "task", "branch"];

/** The ledger grouped by type, moct's vocabulary first, unknown types last. */
function groupByType(records: readonly MemoryRecord[]): [string, MemoryRecord[]][] {
  const groups = new Map<string, MemoryRecord[]>();
  for (const record of records) {
    const group = groups.get(record.type);
    if (group === undefined) groups.set(record.type, [record]);
    else group.push(record);
  }
  return [...groups.entries()].sort((a, b) => {
    const at = ORDERED_TYPES.indexOf(a[0]);
    const bt = ORDERED_TYPES.indexOf(b[0]);
    if (at !== -1 && bt !== -1) return at - bt;
    if (at !== -1) return -1;
    if (bt !== -1) return 1;
    return a[0].localeCompare(b[0]);
  });
}

/** What a decided record's row says happened, in this page's own words. */
function decisionNotice(verb: "approve" | "retract", title: string): string {
  return verb === "approve"
    ? `Approved. ${title} injects from here on.`
    : `Rejected. ${title} stays in the store and does not inject.`;
}

export function AgentMemoryTab({
  agentId,
  connection,
}: {
  agentId: string;
  connection: MoctConnection;
}) {
  const ref = agentRef(agentId);
  const directory = useMemoryDirectory(connection.workspaceId, connection.workspaceSlug);
  const [reloadKey, setReloadKey] = useState(0);
  const overdue = useCallback(() => setReloadKey((current) => current + 1), []);

const wired = connection.problem === null;
const pending = useMemoryPages(pendingListPath, true, wired, reloadKey);
const ledger = useMemoryPages(agentListPath, false, wired, reloadKey);
// Stable across reads: a decision patches one record in either list.
const replacePending = pending.replaceRecord;
const replaceLedger = ledger.replaceRecord;
  const [usage, reloadUsage] = useSeamRead(MOCT_USAGE_PATH, parseMoctUsage, wired);

  // One reload answers every read: this section speaks for one plane, so a
  // reader wants the queue, the ledger and the cap fresher together.
  const reloadAll = useCallback(() => {
    overdue();
    reloadUsage();
  }, [overdue, reloadUsage]);

  // The narrowing is unconditional: a pending row naming this agent is a
  // decision this page is entitled to; nothing else may pass, ever. The status
  // check is the queue's own: a decision replaces the row in place, and a
  // decided record belongs in the ledger, not waiting for approval again.
  const shownPending = useMemo(
    () => pending.records.filter((record) => record.status === "pending" && recordNamesAgent(record, ref)),
    [pending.records, ref],
  );
  const shownRecords = useMemo(
    () => ledger.records.filter((record) => recordNamesAgent(record, ref)),
    [ledger.records, ref],
  );

  const [busyId, setBusyId] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [decisionFailure, setDecisionFailure] = useState<string | null>(null);

  const decide = useCallback(
    async (record: MemoryRecord, verb: "approve" | "retract") => {
      setBusyId(record.id);
      setDecisionFailure(null);
      setNotice(null);
      try {
        const answer = await callJson(
          memoryDecisionPath(record.id, verb),
          parseMemoryRecord,
          { method: "POST", body: { baseVersion: record.version } },
        );
        const refused = refusal(answer);
        if (refused !== null || answer.parsed === null) {
          setDecisionFailure(`${record.title ?? "Untitled record"}: ${refused ?? "the server did not answer"}`);
          return;
        }
        const decided = answer.parsed;
        replacePending(decided);
        replaceLedger(decided);
        setNotice(decisionNotice(verb, decided.title ?? "Untitled record"));
      } finally {
        setBusyId(null);
      }
    },
    // Both reads outlive every decision here; replace is stable per read.
    [replacePending, replaceLedger],
  );

  const ledgerGroups = useMemo(() => groupByType(shownRecords), [shownRecords]);

  // The cap is the seam's answer; the spend side is what this page has actually
  // loaded, which is honest about being page-bounded rather than a server sum.
  const loadedTokens = useMemo(
    () =>
      [...shownPending, ...shownRecords].reduce((sum, record) => sum + record.tokens, 0),
    [shownPending, shownRecords],
  );

  if (connection.problem !== null) {
    return (
      <SettingsCard>
        <SettingsRow label="Memory" align="start">
          <span className="flex flex-col gap-1">
            <Failure message={connection.problem} />
            <Status value="The memory plane is not wired to this panel, so this agent's records cannot be read here." />
          </span>
        </SettingsRow>
      </SettingsCard>
    );
  }

  return (
    <div className="space-y-6" data-agent-memory-tab>
      <SettingsSection
        title="Pending approvals"
        description="A rule, decision or how_to_work this agent wrote lands here first and does not inject until a human decides. The read has no home filter, so a record that merely tags the agent also surfaces — and the list is narrowed to its ref before anything renders."
        action={<RefreshButton onClick={reloadAll} />}
      >
        <SettingsCard>
          {pending.state === "loading" ? (
            <SettingsRow label="Queue">
              <Status value="Loading…" />
            </SettingsRow>
          ) : pending.state === "error" ? (
            <SettingsRow label="Queue">
              <span className="flex items-center gap-2">
                <Failure message={pending.failure ?? "the server did not answer"} />
                <ActionButton label="Retry loading the pending queue" onClick={reloadAll}>
                  Retry
                </ActionButton>
              </span>
            </SettingsRow>
          ) : pending.failure !== null ? (
            <SettingsRow label="Queue">
              <span className="flex items-center gap-2">
                <Failure message={pending.failure} />
                <ActionButton label="Retry loading the pending queue" onClick={reloadAll}>
                  Retry
                </ActionButton>
              </span>
            </SettingsRow>
          ) : shownPending.length === 0 ? (
            <SettingsRow label="Queue">
              <Status
                value={
                  pending.records.length > 0
                    ? "No pending record on the loaded pages names this agent."
                    : "Nothing is waiting for approval."
                }
              />
            </SettingsRow>
          ) : (
            <ul data-agent-pending-list className="divide-y divide-surface-border">
              {shownPending.map((record) => (
                <li key={record.id}>
                  <PendingRow
                    record={record}
                    busy={busyId !== null}
                    onApprove={() => void decide(record, "approve")}
                    onReject={() => void decide(record, "retract")}
                  />
                </li>
              ))}
            </ul>
          )}
          {notice === null ? null : (
            <SettingsRow label="Last decision">
              <span
                role="status"
                data-agent-memory-notice
                className="text-body text-muted-foreground"
              >
                {notice}
              </span>
            </SettingsRow>
          )}
          {decisionFailure === null ? null : (
            <SettingsRow label="Last decision">
              <Failure message={decisionFailure} />
            </SettingsRow>
          )}
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title="Records"
        description="Homed on this agent's profile, every status, grouped by the record's type — moct's vocabulary first, a type this build has never heard of shown as it was sent."
        action={<RefreshButton onClick={reloadAll} />}
      >
        {ledger.state === "loading" ? (
          <SettingsCard>
            <SettingsRow label="Records">
              <Status value="Loading…" />
            </SettingsRow>
          </SettingsCard>
        ) : ledger.state === "error" ? (
          <SettingsCard>
            <SettingsRow label="Records">
              <span className="flex items-center gap-2">
                <Failure message={ledger.failure ?? "the server did not answer"} />
                <ActionButton label="Retry loading the records" onClick={reloadAll}>
                  Retry
                </ActionButton>
              </span>
            </SettingsRow>
          </SettingsCard>
        ) : ledger.failure !== null ? (
          <SettingsCard>
            <SettingsRow label="Page load">
              <span className="flex items-center gap-2">
                <Failure message={ledger.failure} />
                <ActionButton label="Load more records" disabled={ledger.moving} onClick={() => void ledger.loadMore()}>
                  Load more
                </ActionButton>
              </span>
            </SettingsRow>
          </SettingsCard>
        ) : shownRecords.length === 0 ? (
          <SettingsCard>
            <SettingsRow label="Records">
              <Status
                value={
                  ledger.records.length > 0
                    ? "No record on the loaded page is homed on this agent."
                    : "This agent has no records yet."
                }
              />
            </SettingsRow>
          </SettingsCard>
        ) : (
          ledgerGroups.map(([type, group]) => (
            <div key={type} data-agent-record-group={type}>
              <SettingsCard>
                <SettingsRow label={typeLabel(type)}>
                  <Status value={`${group.length} ${group.length === 1 ? "record" : "records"}`} />
                </SettingsRow>
                <ul data-agent-records={type} className="divide-y divide-surface-border">
                  {group.map((record) => (
                    <li key={record.id}>
                      <RecordRow record={record} directory={directory} />
                    </li>
                  ))}
                </ul>
              </SettingsCard>
            </div>
          ))
        )}
        <SettingsCard>
          <SettingsRow label="Count">
            <span className="flex items-center gap-2">
              {ledger.total === null ? null : (
                <Status
                  value={
                    shownRecords.length === ledger.records.length
                      ? `${ledger.records.length} of ${ledger.total} records`
                      : `${shownRecords.length} of the ${ledger.records.length} records loaded (${ledger.total} in total)`
                  }
                />
              )}
              {ledger.cursor === null ? null : (
                <ActionButton
                  label="Load more records"
                  disabled={ledger.moving}
                  onClick={() => void ledger.loadMore()}
                >
                  Load more
                </ActionButton>
              )}
            </span>
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title="Inject budget"
        description="The always-load cap moct reports, beside what the records this page has loaded spend. The spend is computed from the loaded page, not a per-agent server figure."
      >
        <SettingsCard>
          <SettingsRow label="Cap per task">
            <Status
              value={
                usage.state === "ready" && usage.value.capTokens !== null
                  ? `${usage.value.capTokens} tokens`
                  : usage.state === "error"
                    ? "Not reported"
                    : "Loading…"
              }
            />
          </SettingsRow>
          <SettingsRow
            label="Loaded records"
            description={`${shownPending.length} pending · ${shownRecords.length} filed, narrowed to this agent`}
          >
            <Status value={`${loadedTokens} tokens`} />
          </SettingsRow>
          {usage.state === "error" ? (
            <SettingsRow label="Usage">
              <Failure message={usage.message ?? "the server did not answer"} />
            </SettingsRow>
          ) : null}
        </SettingsCard>
      </SettingsSection>
    </div>
  );
}

/**
 * The agent page's Memory section, in the shape the fork's extension point
 * wants: the pane owns how it opens, this owns only what it renders — the same
 * division `createMoctGroup` and `ExtraSettingsGroup` agree on.
 */
export function createAgentMemorySections(connection: MoctConnection): ExtraAgentSection[] {
  return [
    {
      id: "memory",
      label: "Memory",
      render: (agent) => <AgentMemoryTab agentId={agent.id} connection={connection} />,
    },
  ];
}
