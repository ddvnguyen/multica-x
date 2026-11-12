import type { TaskMessagePayload } from "../types/events";

/**
 * A park-and-ask exchange, read off the transcript.
 *
 * A backend that supports user questions (currently the dsh bridge, M-109)
 * parks the run on one of them instead of guessing, and the daemon records the
 * question as its own transcript row: the rendered question in `content` and
 * the structured question list in `input.questions`.
 *
 * Detection is deliberately on the payload rather than on the row's `type`.
 * The server writes `type: "question"`; a client that predates it — including
 * this one's schema — parses that as `text` and renders `content`, which is the
 * full human-readable rendering the daemon still sends for exactly this reason.
 * Keying on `input.questions` therefore finds the ask on both sides of that
 * boundary instead of depending on which client version is reading. When the
 * type is adopted here, only the wire keeps changing: nothing in this module
 * has to.
 */
export interface AgentQuestionOption {
  label: string;
  description?: string;
}

export interface AgentQuestion {
  id: string;
  question: string;
  /** Short category the backend printed above the prompt ("Scope", "Plan"). */
  header?: string;
  /** Long form of the question — for a plan review, the plan itself. */
  detail?: string;
  options: AgentQuestionOption[];
  multiSelect: boolean;
  /** Present on a plan review: `approve` is the label that accepts the plan. */
  intent?: { kind: string; approve: string };
}

/**
 * The option a plan review accepts with, when this question asks for one.
 *
 * The daemon maps an answer whose text equals this label onto the bridge's
 * approval selection; every other answer reaches the backend as the member's
 * own words. So a plan card must send exactly this string to approve, and
 * anything else is read as a revision request.
 */
export function planApprovalLabel(question: AgentQuestion): string | null {
  if (question.intent?.kind !== "plan-review") return null;
  const approve = question.intent.approve.trim();
  return approve === "" ? null : approve;
}

export function isPlanReview(questions: readonly AgentQuestion[]): boolean {
  return questions.length > 0 && questions.every((question) => planApprovalLabel(question) !== null);
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function asText(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function readOptions(value: unknown): AgentQuestionOption[] {
  if (!Array.isArray(value)) return [];
  const options: AgentQuestionOption[] = [];
  for (const entry of value) {
    const record = asRecord(entry);
    const label = asText(record?.label);
    if (label === "") continue;
    const description = asText(record?.description);
    options.push(description === "" ? { label } : { label, description });
  }
  return options;
}

function readQuestion(value: unknown): AgentQuestion | null {
  const record = asRecord(value);
  const prompt = asText(record?.question);
  if (record === null || prompt === "") return null;
  const id = asText(record.id);
  const header = asText(record.header);
  const detail = asText(record.detail);
  const intent = asRecord(record.intent);
  const approve = asText(intent?.approve);
  const kind = asText(intent?.kind);
  return {
    id: id === "" ? prompt : id,
    question: prompt,
    ...(header === "" ? {} : { header }),
    ...(detail === "" ? {} : { detail }),
    options: readOptions(record.options),
    multiSelect: record.multi_select === true,
    ...(kind === "" ? {} : { intent: { kind, approve } }),
  };
}

/**
 * Read the question list off a transcript row's `input`.
 *
 * Returns an empty list for every other kind of row, so a caller can ask the
 * question of any message and get "not an ask" back without branching first.
 * A malformed payload degrades to no card: the rendered `content` is still on
 * screen, so the member loses the form, not the question.
 */
export function parseAgentQuestions(input: unknown): AgentQuestion[] {
  const record = asRecord(input);
  const list = record?.questions;
  if (!Array.isArray(list)) return [];
  const questions: AgentQuestion[] = [];
  for (const entry of list) {
    const question = readQuestion(entry);
    if (question) questions.push(question);
  }
  return questions;
}

/** The newest ask in a transcript, or null when the run never asked one. */
export function latestAgentQuestion(
  messages: readonly TaskMessagePayload[],
): { seq: number; createdAt?: string; questions: AgentQuestion[] } | null {
  let found: { seq: number; createdAt?: string; questions: AgentQuestion[] } | null = null;
  for (const message of messages) {
    const questions = parseAgentQuestions(message.input);
    if (questions.length === 0) continue;
    if (found === null || message.seq >= found.seq) {
      found = { seq: message.seq, createdAt: message.created_at, questions };
    }
  }
  return found;
}

/**
 * Whether the run is still parked on its newest ask.
 *
 * Three signals, in order of what they can prove:
 *
 *  - a run that is not live cannot be waiting for anything;
 *  - anything the run did after the ask — a later transcript row — means it
 *    already moved on, answered or not;
 *  - a delivered steer at or after the ask is the member's answer reaching the
 *    turn, which the run consumes before it writes its next row.
 *
 * The last one is what keeps the form from lingering in the window between the
 * answer being delivered and the backend emitting its first message after it.
 * Undated rows fall through to the message check rather than being treated as
 * answered — a missing timestamp is not evidence.
 */
export function isAgentQuestionPending(
  ask: { seq: number; createdAt?: string } | null,
  messages: readonly TaskMessagePayload[],
  live: boolean,
  deliveredSteers: readonly string[] = [],
): boolean {
  if (ask === null || !live) return false;
  if (messages.some((message) => message.seq > ask.seq)) return false;
  const askedAt = ask.createdAt ? Date.parse(ask.createdAt) : Number.NaN;
  if (!Number.isFinite(askedAt)) return false;
  return !deliveredSteers.some((at) => {
    const delivered = Date.parse(at);
    return Number.isFinite(delivered) && delivered >= askedAt;
  });
}

/** One question's draft in the card: picked options and/or the member's words. */
export interface AgentAnswerDraft {
  selected: readonly number[];
  text: string;
}

function draftText(question: AgentQuestion, draft: AgentAnswerDraft): string {
  const labels = draft.selected
    .map((index) => question.options[index]?.label)
    .filter((label): label is string => label !== undefined);
  const written = draft.text.trim();
  if (labels.length === 0) return written;
  if (written === "") return labels.join(", ");
  // Both parts are the member's answer: keep the picked labels in front so the
  // backend can still read the choice, and let the words carry the nuance.
  return `${labels.join(", ")} — ${written}`;
}

/**
 * The comment text that carries these drafts back into the parked turn.
 *
 * This is the one place the card's form becomes the daemon's `answer` command,
 * and it is deliberately a plain reply: the steer path delivers the member's
 * words, and the daemon's only structural mapping is a plan review answered
 * with exactly its approve label. Everything else reaches the backend as free
 * text, so the text has to read correctly on its own.
 */
export function agentAnswerText(
  questions: readonly AgentQuestion[],
  drafts: readonly AgentAnswerDraft[],
): string {
  return questions
    .map((question, index) => draftText(question, drafts[index] ?? { selected: [], text: "" }))
    .filter((part) => part !== "")
    .join("\n\n");
}

/** A draft with something in it: a pick, or words of the member's own. */
export function isAnsweredDraft(draft: AgentAnswerDraft): boolean {
  return draft.selected.length > 0 || draft.text.trim() !== "";
}

/**
 * The approve reply for a plan review — exactly the label the daemon matches.
 *
 * Null when there is no plan review, or when its questions do not agree on one
 * label: an approval that answers two questions differently has no single
 * reply, and the card must not invent one.
 */
export function planApprovalAnswer(questions: readonly AgentQuestion[]): string | null {
  const labels = questions.map(planApprovalLabel);
  const first = labels[0];
  if (first === undefined || first === null) return null;
  return labels.every((label) => label === first) ? first : null;
}
