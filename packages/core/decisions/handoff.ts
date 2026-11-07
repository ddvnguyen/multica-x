import type { TimelineEntry } from "../types";

/**
 * Answering a `wait_for_decision` handoff in place (M-274).
 *
 * A handoff has no body of its own: the question and its choices live in the
 * comment the agent wrote before it parked the issue, and the destination is a
 * workflow status. Two readers here turn the comment and the issue timeline into
 * what the form renders, and one writer shapes the request the server's
 * `decision-response` transaction expects (M-209).
 *
 * The block is a fenced convention, not a schema the server validates. A
 * comment without one — every handoff written before this card — still has to
 * be answerable, so `parseDecisionBlock` returns null and the form falls back
 * to the default choices rather than refusing to render.
 */

/** One choice a grilling comment offers. */
export interface HandoffOption {
  /** Stable key inside the block; what `default` names. */
  key: string;
  /** Short label — this is what reaches the server as `decision`. */
  label: string;
  /** One line of context under the label, or "". */
  detail: string;
  /** The step choosing this option asks for, or null to use the selected one. */
  nextStatus: string | null;
}

export interface DecisionBlock {
  /** The one-line question, or "" when the comment itself is the question. */
  question: string;
  options: HandoffOption[];
  /** The option key the comment recommends, or null. */
  defaultKey: string | null;
}

const FENCE = /```decision[ \t]*\r?\n([\s\S]*?)```/;
const BULLET = /^[-*]\s+(.*)$/;

function unquote(value: string): string {
  const trimmed = value.trim();
  if (trimmed.length < 2) return trimmed;
  const first = trimmed[0];
  const last = trimmed[trimmed.length - 1];
  return (first === `"` || first === `'`) && first === last
    ? trimmed.slice(1, -1)
    : trimmed;
}

/** `key: value` at the start of a line, or a null field for bare text. */
function splitField(line: string): { field: string | null; value: string } {
  const at = line.indexOf(":");
  if (at <= 0) return { field: null, value: line };
  const field = line.slice(0, at).trim().toLowerCase().replace(/[- ]/g, "_");
  if (!/^[a-z_]+$/.test(field)) return { field: null, value: line };
  return { field, value: unquote(line.slice(at + 1)) };
}

function put(option: HandoffOption, field: string | null, value: string): void {
  switch (field) {
    case "key":
      option.key = value;
      return;
    case "label":
      option.label = value;
      return;
    case "detail":
    case "description":
      option.detail = value;
      return;
    case "next_status":
    case "status":
      option.nextStatus = value === "" ? null : value;
      return;
    default:
      // A bullet without a field of its own is a label: `- Expand first`.
      if (field === null && option.label === "") option.label = value;
  }
}

/**
 * The `decision` block a grilling comment carries, or null.
 *
 * ```decision
 * question: Which way should the migration go?
 * options:
 *   - key: expand
 *     label: Expand, then contract
 *     detail: Two deploys, no downtime
 *     next_status: in_progress
 * default: expand
 * ```
 *
 * Tolerant on purpose: unknown fields are ignored, quotes are stripped, and a
 * bullet with no `key` gets its position as one, so a slightly malformed block
 * still answers instead of degrading the whole card to free text.
 */
export function parseDecisionBlock(markdown: string): DecisionBlock | null {
  const match = FENCE.exec(markdown);
  if (match === null) return null;
  const body = match[1] ?? "";
  let question = "";
  let defaultKey: string | null = null;
  const options: HandoffOption[] = [];
  let current: HandoffOption | null = null;

  for (const raw of body.split(/\r?\n/)) {
    const line = raw.trim();
    if (line === "" || line.startsWith("#")) continue;
    const bullet = BULLET.exec(line);
    if (bullet !== null) {
      current = { key: "", label: "", detail: "", nextStatus: null };
      options.push(current);
      const { field, value } = splitField(bullet[1] ?? "");
      put(current, field, value);
      continue;
    }
    const { field, value } = splitField(line);
    if (field === "options") {
      current = null;
      continue;
    }
    if (current !== null && field !== "question" && field !== "default") {
      put(current, field, value);
      continue;
    }
    if (field === "question") question = value;
    else if (field === "default") defaultKey = value === "" ? null : value;
    else if (field === null && current !== null && current.label === "") {
      current.label = value;
    }
  }

  const kept = options.filter((option) => option.label !== "");
  if (kept.length === 0) return null;
  for (const [index, option] of kept.entries()) {
    if (option.key === "") option.key = String(index + 1);
  }
  const named =
    defaultKey === null
      ? null
      : kept.find((option) => option.key === defaultKey)?.key ??
        kept.find((option) => option.label === defaultKey)?.key ??
        null;
  return { question, options: kept, defaultKey: named };
}

/** The comment's prose without the machine-readable block it also carries. */
export function stripDecisionBlock(markdown: string): string {
  return markdown.replace(FENCE, "").trim();
}

export interface HandoffComment {
  content: string;
  createdAt: string;
}

export interface HandoffContext {
  /** The most recent comment an agent wrote, or null. */
  comment: HandoffComment | null;
  /**
   * The status the issue was in immediately before it entered the waiting step,
   * or null when the timeline does not say (an imported issue, a pruned
   * activity). The form then requires an explicit destination instead of
   * guessing one that could start an agent.
   */
  priorStatus: string | null;
}

/**
 * Read a waiting issue's own timeline.
 *
 * The queue row is a projection of the issue's state, so the question is not in
 * the detail payload: it is the agent's last comment, and the default
 * destination is the transition into the waiting step. Both are here rather
 * than in the handler because they are presentation reads of data the client
 * already fetches.
 */
export function readHandoffContext(
  entries: readonly TimelineEntry[],
  waitingStatus: string,
): HandoffContext {
  let comment: HandoffComment | null = null;
  let priorStatus: string | null = null;
  for (const entry of entries) {
    if (entry.type === "comment" && entry.actor_type === "agent") {
      const content = entry.content ?? "";
      if (content.trim() !== "") comment = { content, createdAt: entry.created_at };
      continue;
    }
    if (entry.type !== "activity" || entry.action !== "status_changed") continue;
    const details = entry.details ?? {};
    if (details.to !== waitingStatus) continue;
    if (typeof details.from !== "string" || details.from === "") continue;
    priorStatus = details.from;
  }
  return { comment, priorStatus };
}

/**
 * What reaches the server as `decision`.
 *
 * A picked option sends its label, which is what the agent's grilling comment
 * offers in words; a free-text answer without one sends `custom`, because the
 * text itself is the answer and it travels as the rationale.
 */
export function handoffDecision(
  option: HandoffOption | null,
  text: string,
): string | null {
  if (option !== null) return option.label;
  return text.trim() === "" ? null : "custom";
}
