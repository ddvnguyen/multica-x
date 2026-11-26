-- Jev shadow agreement report (M-588 B3a / M-592).
--
-- One row per (decision_point, question_key): how many observations shadow
-- recorded, how many Jev and the product agreed on, how many they disagreed
-- on, how many calls failed, mean confidence (choice rows only), mean latency
-- and summed tokens. This is the evidence the Leader needs to switch a
-- decision live (at least one week of rows per point).
--
-- stall_resume_reclaim is a task-level recovery of a dispatched, never-started
-- task — NOT the Owner's card-level "resume this stalled card". Only reclaimed
-- tasks produce rows, so agreement here measures calibration of "will it start
-- and act", with no negative class: a reclaimed task that starts within $1
-- minutes and runs cleanly counts as agreed-with-act; anything else counts as
-- disagreed. There is no "would not have resumed" counterfactual.
--
-- triage_route_assign verdicts are Jev's pick (engineer/reviewer/architect/
-- leader/owner). The actual route is normalized from the assignee in the same
-- query: agents whose role/name maps to no label count as 'other', never as
-- disagreed. jev-shadow-route-detail.sql shows raw assignees next to the
-- normalized route so a mismatch is auditable, not silent.
--
-- issue_wakeup_dispatch rows report volume, confidence and latency only: the
-- run-activity label join they would need is not defined yet, so no
-- agreement column is computed for them.
--
-- Usage: psql $DATABASE_URL -v resume_minutes=15 -f server/scripts/jev-shadow-agreement.sql
-- (the file uses $1; pass the window as the first bind parameter, or wrap it:
-- SELECT * FROM ... WHERE ... with $1 = 15).

WITH shadow AS (
    SELECT decision_point,
           question_key,
           verdict,
           answer,
           usage,
           latency_ms,
           error_kind,
           model,
           issue_id,
           subject_id,
           created_at
      FROM jev_shadow_decision
),
with_routes AS (
    -- Actual route per observation, normalized once from the assignee.
    -- Role/name→label lookup shared with routeActorIsRouter in jev_shadow.go:
    -- unmapped assignees count as 'other', never as disagreed.
    SELECT s.*,
           COALESCE((
               SELECT CASE
                   WHEN lower(a.name) LIKE '%orchestrator%' THEN 'owner'
                   WHEN lower(a.name) LIKE '%architect%' THEN 'architect'
                   WHEN lower(a.name) LIKE '%review%' THEN 'reviewer'
                   WHEN lower(a.name) LIKE '%engineer%' THEN 'engineer'
                   WHEN lower(a.name) LIKE '%leader%' OR lower(a.name) LIKE '%lead%' THEN 'leader'
                   WHEN a.kind = 'role' THEN 'engineer'
                   ELSE 'other'
               END
               FROM issue i LEFT JOIN agent a ON a.id = i.assignee_id
               WHERE i.id = s.issue_id
           ), 'other') AS actual_route
      FROM shadow s
),
agreement AS (
    SELECT s.*,
           CASE
               WHEN s.decision_point = 'stall_resume_reclaim' AND s.error_kind IS NULL THEN
                   CASE
                       WHEN EXISTS (
                           SELECT 1 FROM agent_task_queue t
                            WHERE t.id = s.subject_id
                              AND t.started_at IS NOT NULL
                              AND t.started_at <= s.created_at + (make_interval(mins => $1)::interval)
                              AND (t.error IS NULL OR t.error = '')
                       ) AND s.verdict = 'act' THEN 'agreed'
                       WHEN s.verdict = 'act' THEN 'disagreed'
                       ELSE NULL -- no_action verdicts have no negative class
                   END
               WHEN s.decision_point = 'triage_route_assign' AND s.error_kind IS NULL THEN
                   CASE
                       WHEN s.actual_route = 'other' THEN 'unmapped'
                       WHEN s.actual_route = s.verdict THEN 'agreed'
                       ELSE 'disagreed'
                   END
               ELSE NULL -- wakeup rows: volume/confidence/latency only
           END AS agreement
      FROM with_routes s
)
SELECT decision_point,
       question_key,
       count(*) AS total,
       count(*) FILTER (WHERE agreement = 'agreed') AS agreed,
       count(*) FILTER (WHERE agreement = 'disagreed') AS disagreed,
       count(*) FILTER (WHERE agreement = 'unmapped') AS unmapped,
       count(*) FILTER (WHERE error_kind IS NOT NULL) AS errors,
       round(avg((answer->>'confidence')::numeric) FILTER (WHERE answer ? 'confidence'), 3) AS mean_confidence,
       round(avg(latency_ms)::numeric, 1) AS mean_latency_ms,
       coalesce(sum(((usage->>'input_tokens')::bigint + (usage->>'output_tokens')::bigint)) FILTER (WHERE usage ? 'input_tokens'), 0) AS total_tokens
  FROM agreement
 GROUP BY decision_point, question_key
 ORDER BY decision_point, question_key;
