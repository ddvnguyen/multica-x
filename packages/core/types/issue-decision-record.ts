export interface IssueDecisionRecord {
  id: string;
  issue_id: string;
  comment_id: string;
  decided: string;
  rationale: string;
  supersedes_id: string | null;
  superseded_by_id: string | null;
  source_kind: "comment" | "ask_answer" | "plan_approval" | string;
  actor_id: string;
  created_at: string;
}
