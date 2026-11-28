-- name: UpsertTaskMemoryMark :one
-- One judgement per run: a member changing their mind replaces the row rather
-- than stacking a second vote, so the aggregate counts runs, not clicks.
INSERT INTO task_memory_mark (
    task_id, workspace_id, choice, marked_by_type, marked_by_id,
    primary_bytes, primary_latency_ms, shadow_bytes, shadow_latency_ms, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, now()
)
ON CONFLICT (task_id) DO UPDATE SET
    choice = EXCLUDED.choice,
    marked_by_type = EXCLUDED.marked_by_type,
    marked_by_id = EXCLUDED.marked_by_id,
    primary_bytes = EXCLUDED.primary_bytes,
    primary_latency_ms = EXCLUDED.primary_latency_ms,
    shadow_bytes = EXCLUDED.shadow_bytes,
    shadow_latency_ms = EXCLUDED.shadow_latency_ms,
    updated_at = now()
RETURNING *;

-- name: GetTaskMemoryMark :one
SELECT * FROM task_memory_mark WHERE task_id = $1 AND workspace_id = $2;

-- name: GetWorkspaceMemoryMarkStats :one
-- The workspace aggregate behind the compare panel: how the votes split, and
-- the median size and latency each backend cost on the runs that were judged.
-- Medians ignore unmarked runs on purpose — a run nobody judged is not evidence
-- either way, and its numbers would otherwise dominate the sample.
--
-- M-638: an optional lower bound on the vote's own timestamp lets the CLI's
-- --since window be applied server-side, so two clients asking for the same
-- window see the same counts. A null bound keeps the panel's all-time view.
-- The shadow counts describe the runs that were judged: a shadow that returned
-- a block against one that recorded no block is the difference between a
-- comparison that happened and one that only had an error or a note.
SELECT
    count(*)::bigint AS marked,
    count(*) FILTER (WHERE choice = 'moct')::bigint AS moct_votes,
    count(*) FILTER (WHERE choice = 'powercontext')::bigint AS powercontext_votes,
    count(*) FILTER (WHERE choice = 'neither')::bigint AS neither_votes,
    count(*) FILTER (WHERE shadow_bytes > 0)::bigint AS shadow_with_block,
    count(*) FILTER (WHERE shadow_bytes = 0)::bigint AS shadow_without_block,
-- COALESCE keeps an unmarked workspace a zero row instead of a NULL scan.
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY primary_bytes), 0)::float8 AS median_moct_bytes,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY primary_latency_ms), 0)::float8 AS median_moct_latency_ms,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY shadow_bytes), 0)::float8 AS median_powercontext_bytes,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY shadow_latency_ms), 0)::float8 AS median_powercontext_latency_ms
FROM task_memory_mark
WHERE workspace_id = $1
  AND updated_at >= COALESCE(sqlc.narg('since')::timestamptz, '-infinity'::timestamptz);
