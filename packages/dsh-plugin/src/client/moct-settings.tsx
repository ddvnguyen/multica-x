import { useEffect, useState, type ReactNode } from "react";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
  SettingsTab,
  type ExtraSettingsGroup,
} from "../fork";

/**
 * The MOCT group in the settings page.
 *
 * S2-B puts the fork's real `SettingsPage` in this panel's sixth rail row. The
 * page is the web app's, so almost every group in it is about Multica itself;
 * this module adds the one group that only exists in dsh — the memory plane the
 * panel reaches through `/moct` (S2-A's closed seam).
 *
 * Everything here is read-only. The seam exposes memory verbs (approve,
 * retract) but a settings page is the wrong place to decide a record, and the
 * host proxy gates those verbs on a workspace admin; showing the plane's
 * health and its context budget is what a reader comes here for. No write, so
 * no optimistic state to reconcile.
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
  /** Same-origin realtime path. */
  wsPath: string;
  /** Why the host cannot proxy, when it cannot. Never a credential. */
  problem: string | null;
}

export const MOCT_STATUS_PATH = "/moct/status";
export const MOCT_USAGE_PATH = "/moct/team/v1/memory/usage";

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

type Load<T> =
  | { state: "loading" }
  | { state: "ready"; value: T }
  | { state: "error"; message: string };

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

function Status({ value }: { value: string }) {
  return <span className="text-body text-muted-foreground">{value}</span>;
}

/** The memory plane: reachability, schema, and where the budget is spent. */
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

/** How this panel is wired: the workspace it speaks as and the seam's state. */
export function MoctConnectionTab({ connection }: { connection: MoctConnection }) {
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

/**
 * The MOCT group, as `SettingsPage` wants it.
 *
 * Built once per connection: the tab content reads the seam on mount, so
 * nothing here touches the network until a reader opens it.
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
    ],
  };
}
