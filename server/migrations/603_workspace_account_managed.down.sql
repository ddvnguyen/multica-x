ALTER TABLE workspace_account DROP CONSTRAINT IF EXISTS workspace_account_state_check;
DROP INDEX IF EXISTS workspace_account_runtime_id_idx;
DROP INDEX IF EXISTS workspace_account_daemon_account_id_uq;
ALTER TABLE workspace_account
    DROP COLUMN IF EXISTS runtime_id,
    DROP COLUMN IF EXISTS daemon_account_id,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS error,
    DROP COLUMN IF EXISTS expires_at,
    DROP COLUMN IF EXISTS login_url,
    DROP COLUMN IF EXISTS state;
