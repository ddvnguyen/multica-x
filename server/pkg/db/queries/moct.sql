-- M-570: idempotency gate for the pending-memory inbox sweeper. One row per
-- announced (workspace, record); decided or purged records leave moct-be's
-- pending set, so rows here never need a cleanup sweep.

-- name: ListMoctNotifiedRecordIDs :many
SELECT record_id FROM moct_pending_notified WHERE workspace_id = $1;

-- name: MarkMoctPendingNotified :execrows
INSERT INTO moct_pending_notified (workspace_id, record_id)
VALUES ($1, $2)
ON CONFLICT (workspace_id, record_id) DO NOTHING;

-- name: ListMoctSweepWorkspaceIDs :many
-- Every workspace the pending-memory sweeper polls: one row per workspace,
-- oldest first. The sweeper skips hosts with no MOCT_BASE_URL before calling
-- moct-be, so an unconfigured host costs one cheap scan per tick.
SELECT DISTINCT workspace_id FROM member ORDER BY workspace_id ASC;
