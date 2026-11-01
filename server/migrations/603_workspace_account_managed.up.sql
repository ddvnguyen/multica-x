-- M-202: daemon-managed freebuff-desktop accounts (browser login).
-- Adds lifecycle state, daemon correlation and owning runtime to workspace_account.
ALTER TABLE workspace_account
    ADD COLUMN state TEXT NOT NULL DEFAULT 'connected',
    ADD COLUMN login_url TEXT,
    ADD COLUMN expires_at TIMESTAMPTZ,
    ADD COLUMN error TEXT,
    ADD COLUMN status JSONB,
    ADD COLUMN daemon_account_id TEXT,
    ADD COLUMN runtime_id UUID REFERENCES agent_runtime(id) ON DELETE SET NULL;

CREATE UNIQUE INDEX workspace_account_daemon_account_id_uq
    ON workspace_account (workspace_id, daemon_account_id)
    WHERE daemon_account_id IS NOT NULL;

CREATE INDEX workspace_account_runtime_id_idx
    ON workspace_account (runtime_id);

ALTER TABLE workspace_account
    ADD CONSTRAINT workspace_account_state_check
        CHECK (state IN ('requested','awaiting_login','connected','signed_out','failed','removing'));
