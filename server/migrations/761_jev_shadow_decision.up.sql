-- Jev shadow decisions (M-574 A4 / M-580): one row per orchestration decision a
-- Jev answer was logged next to, so the answer and the decision the product
-- actually made can be compared later.
--
-- Nothing reads this table on a request path. It exists to be joined: the card,
-- the moment, and what Jev said, next to whatever the run that followed did.
-- Rows are written only when MULTICA_JEV_SHADOW is on and a key resolves, so a
-- deployment without either never writes here.
CREATE TABLE jev_shadow_decision (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    -- decision_point names the fork being observed, e.g. issue_wakeup_dispatch.
    decision_point text NOT NULL,
    -- subject is what was judged (a wakeup rule); issue_id is the card whose
    -- outcome is the label, kept explicit so the comparison is a join rather
    -- than an exercise in reading jsonb.
    subject_type text NOT NULL,
    subject_id uuid,
    issue_id uuid,
    question_key text NOT NULL,
    -- answer is the vendor's answer object as sent; NULL with error_kind set
    -- when the call failed.
    answer jsonb,
    threshold real,
    verdict text,
    model text,
    usage jsonb,
    latency_ms integer,
    error_kind text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX jev_shadow_decision_point_created_idx
    ON jev_shadow_decision (decision_point, created_at DESC);
