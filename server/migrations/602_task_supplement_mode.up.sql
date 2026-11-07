-- Self-idempotent: lanes that ran this SQL as 590_task_supplement_mode
-- already have the column and the check, and the rename gate
-- (whenRenamedObjectsAllPresent) already records 602 without re-running.
-- These guards keep the migration safe even if the gate is bypassed or the
-- objects were partially applied: never fail with "already exists".
ALTER TABLE task_supplement
    ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT 'steer';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'task_supplement_mode_check'
          AND conrelid = 'task_supplement'::regclass
    ) THEN
        ALTER TABLE task_supplement
            ADD CONSTRAINT task_supplement_mode_check CHECK (mode IN ('steer', 'queue'));
    END IF;
END
$$;
