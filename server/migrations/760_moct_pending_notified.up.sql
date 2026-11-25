-- M-570: one inbox row per pending memory record. The sweeper polls
-- moct-be for status=pending and notifies workspace managers; this table is
-- the idempotency gate so a still-pending record is announced once.
-- Decided or purged records leave the pending set, so no cleanup sweep is
-- needed: rows only grow by (workspace, record) pairs moct-be actually held.
CREATE TABLE moct_pending_notified (
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    record_id TEXT NOT NULL,
    notified_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, record_id)
);
