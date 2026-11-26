-- Jev shadow route detail (M-588 B3a / M-592): recent triage_route_assign rows
-- with Jev's pick, the normalized actual route, and the raw assignee — so a
-- normalization mismatch is auditable, not silent. Latest first, 100 rows.
--
-- Usage: psql $DATABASE_URL -f server/scripts/jev-shadow-route-detail.sql

SELECT s.created_at,
       s.verdict AS jev_pick,
       (s.answer->>'confidence')::numeric AS confidence,
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
       ), 'other') AS actual_route,
       i.assignee_type AS raw_assignee_type,
       a.name AS raw_assignee_name,
       s.latency_ms
  FROM jev_shadow_decision s
  LEFT JOIN issue i ON i.id = s.issue_id
  LEFT JOIN agent a ON a.id = i.assignee_id
 WHERE s.decision_point = 'triage_route_assign'
 ORDER BY s.created_at DESC
 LIMIT 100;
