// Structured agent questions (M-81).
//
// A run whose tool call needs a decision parks it and records a question on
// the issue. The same record is read by two surfaces — the issue card a human
// answers, and `multica question` for an agent answering on the owner's
// behalf — so the shape is deliberately runtime-neutral: nothing here says
// CodeBuddy, and claude/omp/pi can produce it unchanged.

export type QuestionStatus = "open" | "answered" | "expired";

/** One choice the asking tool offered. `description` is the provider's hint. */
export interface QuestionOption {
  label: string;
  description?: string;
}

export interface Question {
  id: string;
  /** Questions asked by one tool call share a group and are answered together. */
  group_id: string;
  issue_id?: string;
  task_id?: string;
  /** Provider-side tool call identity, for correlating with the transcript. */
  tool_call_id?: string;
  asked_by_agent_id?: string;
  addressee_type?: "member" | "agent";
  addressee_id?: string;
  header: string;
  prompt: string;
  options: QuestionOption[];
  multi_select: boolean;
  status: QuestionStatus;
  answer?: string;
  /** Who settled it: an agent answering via CLI is attributed to the agent. */
  answered_by_type?: "member" | "agent";
  answered_by_id?: string;
  answered_at?: string;
  expires_at: string;
  created_at: string;
}
