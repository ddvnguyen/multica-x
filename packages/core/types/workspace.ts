export type MemberRole = "owner" | "admin" | "member";

export interface WorkspaceRepo {
  url: string;
  description?: string;
}

export interface Workspace {
  id: string;
  name: string;
  slug: string;
  description: string | null;
  context: string | null;
  settings: Record<string, unknown>;
  repos: WorkspaceRepo[];
  issue_prefix: string;
  avatar_url: string | null;
  created_at: string;
  updated_at: string;
  pinned_at?: string | null;
}

/**
 * One MCP server in the workspace's library.
 *
 * This is the WHOLE read shape: the stored configuration is write-only, so
 * urls, commands, headers, and env never leave the server for any role. UI
 * that needs to change an entry sends a replacement rather than editing what
 * it read back.
 *
 * `enabled` is only present on an AGENT's assignment list, where it is the
 * per-agent toggle; the workspace library listing has no binding to report.
 */
export interface WorkspaceMcpServer {
  id: string;
  workspace_id: string;
  name: string;
  transport: string;
  enabled?: boolean;
  /**
   * Live agents the entry is assigned to. Only the workspace library listing
   * carries it, and servers older than this field omit it.
   */
  agent_count?: number;
  created_at: string;
  updated_at: string;
}

/**
 * One workspace model tier (M-350). `idx` is the fallback order (0 first,
 * 99 last); `provider_slots` is the max simultaneous sessions on that
 * runtime/provider (e.g. Freebuff = 1), not an agent concurrency cap.
 * `cost` arrives as a plain decimal string; `quality` is 0–100.
 * Resolution at enqueue is M-351 — this card is the curated list only.
 */
export interface ModelTier {
  id: string;
  workspace_id: string;
  name: string;
  idx: number;
  runtime_id?: string;
  provider: string;
  model: string;
  thinking_level?: string;
  service_tier?: string;
  cost?: string;
  quality?: number;
  provider_slots: number;
  enabled: boolean;
  /** Roles served (M-423). Empty = legacy/unconfigured. */
  roles: string[];
  /** Per-role ordering and auto-pick state (M-550); optional for older APIs. */
  role_settings?: ModelTierRoleSetting[];
  /**
   * Position inside the requested role's list (M-497). Present only on
   * role-filtered (?role=) reads and role reorder responses.
   */
  role_idx?: number;
  /** Never auto-resolves; explicit picks need a workspace owner. */
  manual_only: boolean;
  /**
   * M-481: true when scheduling may pick this tier by idx, quota and quality.
   * False (legacy `manual_only`) only serves explicit `@agent#tier` picks.
   */
  auto_pick: boolean;
  /** Scheduling hint; the resolver ignores it. */
  quota_kind: string;
  /** Multi-select quota kinds (M-481); `quota_kind` stays the legacy scalar. */
  quota_kinds: string[];
  is_free: boolean;
  /** Context ceiling in tokens (default 280000); advisory per harness. */
  ctx_max_tokens: number;
  /** Compaction trigger as a percentage of `ctx_max_tokens` (default 75). */
  compact_at_pct: number;
  /**
   * Provider-refusal cooldown deadline (M-352). Present only while a 429/quota
   * failure has the tier out of the resolver's idx order; the Settings row
   * renders it as "cooling down until HH:MM".
   */
  cooldown_until?: string;
  created_at: string;
  updated_at: string;
}

export interface ModelTierRoleSetting {
  role: string;
  auto_pick: boolean;
  idx: number;
}

export interface ModelTierInput {
  name?: string;
  idx?: number;
  /** Omitted = keep; empty string = unbind the runtime. */
  runtime_id?: string;
  provider?: string;
  model?: string;
  thinking_level?: string;
  service_tier?: string;
  /** Number or decimal string; omitted = keep. */
  cost?: number | string;
  /**
   * 0–100; explicit `null` clears to unknown on update. Omitted keeps the
   * value on update (NULL by default on create). 0 is a legitimate measured
   * floor, never unknown.
   */
  quality?: number | null;
  provider_slots?: number;
  enabled?: boolean;
  roles?: string[];
  /** Additive role settings; `roles` remains for older clients. */
  role_settings?: ModelTierRoleSetting[];
  manual_only?: boolean;
  auto_pick?: boolean;
  quota_kind?: string;
  quota_kinds?: string[];
  is_free?: boolean;
  ctx_max_tokens?: number;
  compact_at_pct?: number;
}

/**
 * One (harness, model) mapping inside a tier profile (M-481). Position is the
 * pick order: the resolver tries the first available member, so a 429 on one
 * member only cools that member instead of the whole tier.
 */
export interface ModelTierMember {
  id: string;
  tier_id: string;
  position: number;
  provider: string;
  model: string;
  thinking_level?: string;
  runtime_id?: string;
  provider_slots: number;
  cooldown_until?: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface ModelTierMemberInput {
  position?: number;
  provider?: string;
  model?: string;
  thinking_level?: string;
  /** Empty string unbinds the runtime. */
  runtime_id?: string;
  provider_slots?: number;
  enabled?: boolean;
}

/**
 * One entry of the workspace role registry (M-497, Owner Q4). Keys are free
 * workspace-defined strings; the registry seeds the three classic roles.
 */
export interface TierRole {
  key: string;
  position: number;
}

/**
 * One skill in the role composition preview. Source is "role" for a skill
 * the Agent inherits from its Role, "agent" for one it owns itself; an ID
 * present on both is reported once with source "role".
 */
export interface RolePreviewSkill {
  id: string;
  name: string;
  description: string;
  source: string;
}

/**
 * Read-only inherited view for an Agent implementing a Role (M-499 slice 4,
 * `GET /api/agents/{id}/role-preview`). Role instructions first, the
 * Agent's own additions after, and the union of both agents' enabled
 * skills. Field names match the backend JSON tags exactly.
 */
export interface RoleCompositionPreview {
  agent_id: string;
  role_id?: string | null;
  role_name?: string;
  instructions: string;
  role_instructions?: string;
  agent_instructions: string;
  skills: RolePreviewSkill[];
}

/** A named agent+tier quick pick. */
export interface AgentPreset {
  id: string;
  workspace_id: string;
  name: string;
  agent_id: string;
  agent_name: string;
  tier_id: string;
  tier_name: string;
  tier_enabled: boolean;
}

export interface Member {
  id: string;
  workspace_id: string;
  user_id: string;
  role: MemberRole;
  created_at: string;
}

export interface User {
  id: string;
  name: string;
  email: string;
  avatar_url: string | null;
  onboarded_at: string | null;
  /**
   * JSONB payload from the server. Typed as `unknown` here so this module
   * stays independent of the questionnaire shape — the onboarding views
   * cast into `Partial<QuestionnaireAnswers>` when reading. Server always
   * returns an object (defaults to `{}`), never null.
   */
  onboarding_questionnaire: Record<string, unknown>;
  /** Preferred UI language. null means "follow client/system". */
  language: string | null;
  /**
   * Free-form self-description (role, stack, preferences). Injected into
   * the agent brief so coding agents have cheap, durable context about
   * who is requesting the work. Server always returns a string —
   * NOT NULL DEFAULT '' at the column level, empty when unset.
   */
  profile_description: string;
  /** Pinned IANA tz; null means "use browser-detected tz at render time". */
  timezone: string | null;
  /**
   * Newest release whose What's new highlights the user has dismissed.
   * null = never dismissed; undefined = the server predates read tracking,
   * so the client must not auto-open the card (it would reappear forever).
   */
  whats_new_seen_version?: string | null;
  /**
   * "Star us on GitHub" prompt bookkeeping. An undefined count means the
   * server predates it, so the client must never show the prompt.
   */
  star_prompt_shown_count?: number;
  star_prompt_last_shown_at?: string | null;
  star_prompt_starred_at?: string | null;
  created_at: string;
  updated_at: string;
}

export interface MemberWithUser {
  id: string;
  workspace_id: string;
  user_id: string;
  role: MemberRole;
  created_at: string;
  name: string;
  email: string;
  avatar_url: string | null;
}

export interface Invitation {
  id: string;
  workspace_id: string;
  inviter_id: string;
  invitee_email: string;
  invitee_user_id: string | null;
  role: MemberRole;
  status: "pending" | "accepted" | "declined" | "expired";
  created_at: string;
  updated_at: string;
  expires_at: string;
  inviter_name?: string;
  inviter_email?: string;
  workspace_name?: string;
}

export interface ShareLink {
  id: string;
  workspace_id: string;
  code: string;
  created_by: string;
  role: MemberRole;
  expires_at: string | null;
  max_uses: number | null;
  use_count: number;
  is_active: boolean;
  created_at: string;
  creator_name?: string;
  creator_email?: string;
}

export interface ShareLinkInfo {
  workspace_name: string;
  workspace_slug: string;
  creator_name?: string;
  role: MemberRole;
}
