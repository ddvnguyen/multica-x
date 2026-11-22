-- M-471: audited, lane-z-only data seed for the M-467 agent preset rollout.
--
-- First run with -v apply=false and save stdout as the reviewed snapshot. Check
-- every profile/tier row against the lane before re-running with -v apply=true.
-- The script revalidates all rows inside the apply transaction. Rollback is to
-- set workspace.settings.agent_presets_enabled=false; no agent/task/mention
-- identity rows are deleted or rewritten.
--
-- Example (run from the repository root, with the lane-z DATABASE_URL):
--   psql "$DATABASE_URL" -X -v ON_ERROR_STOP=1 \
--     -v workspace_id='<lane-z-workspace-uuid>' -v apply=false \
--     -f scripts/seed-agent-presets.sql > agent-preset-seed-snapshot.tsv
-- Review the snapshot, then repeat with -v apply=true -v snapshot_reviewed=true.

\set ON_ERROR_STOP on
\if :{?workspace_id}
\else
  \echo 'workspace_id is required'
  \quit 2
\endif
\if :{?apply}
\else
  \set apply false
\endif
\if :{?snapshot_reviewed}
\else
  \set snapshot_reviewed false
\endif
\if :apply
  \if :snapshot_reviewed
  \else
    \echo 'snapshot_reviewed=true is required before apply=true'
    \quit 3
  \endif
\endif

BEGIN;
SELECT set_config('m471.workspace_id', :'workspace_id', true);

CREATE TEMP TABLE m471_profile_map (
    legacy_agent_id UUID PRIMARY KEY,
    legacy_name TEXT NOT NULL,
    position_agent_id UUID NOT NULL,
    position_name TEXT NOT NULL,
    position_role TEXT NOT NULL,
    tier_name TEXT NOT NULL
) ON COMMIT DROP;

INSERT INTO m471_profile_map VALUES
    ('96a1cd5e-2a6e-4ced-a8b3-c28d413e956e', 'Architect', '96a1cd5e-2a6e-4ced-a8b3-c28d413e956e', 'Architect', 'architect', 'arch-glm-flash'),
    ('bd7b7aae-88e9-4260-a7b5-301993757e59', 'Claude Architect', '96a1cd5e-2a6e-4ced-a8b3-c28d413e956e', 'Architect', 'architect', 'arch-sonnet-5-5'),
    ('7895c8ca-aed6-461e-9fea-a115b2681872', 'Opus Architect', '96a1cd5e-2a6e-4ced-a8b3-c28d413e956e', 'Architect', 'architect', 'arch-opus-5-5'),
    ('0e86b49e-1c6a-4a86-93a6-b602b9a86eae', 'Orchestrator', '0e86b49e-1c6a-4a86-93a6-b602b9a86eae', 'Orchestrator', 'management', 'orch-beam'),
    ('1f23308e-83e4-4923-9ff6-2fc7ce80d841', '[Go Free] Engineer', 'd5c380b6-590a-498f-8591-557c05f462ec', 'Software Engineer', 'engineer', 'eng-beam'),
    ('a62593fd-09b4-4fe6-aae9-5351829e8a7d', '[Free] Codebuddy Engineer', 'a62593fd-09b4-4fe6-aae9-5351829e8a7d', '[Free] Codebuddy Engineer', 'engineer', 'eng-cb-deepseek');

CREATE TEMP TABLE m471_positions (
    position_agent_id UUID PRIMARY KEY,
    position_name TEXT NOT NULL,
    position_role TEXT NOT NULL
) ON COMMIT DROP;

INSERT INTO m471_positions VALUES
    ('96a1cd5e-2a6e-4ced-a8b3-c28d413e956e', 'Architect', 'architect'),
    ('2bd96ccd-0e7a-48cb-96d4-4e90f334b6fe', 'Lead', 'management'),
    ('0e86b49e-1c6a-4a86-93a6-b602b9a86eae', 'Orchestrator', 'management'),
    ('d5c380b6-590a-498f-8591-557c05f462ec', 'Software Engineer', 'engineer'),
    ('a62593fd-09b4-4fe6-aae9-5351829e8a7d', '[Free] Codebuddy Engineer', 'engineer');

CREATE TEMP TABLE m471_seed_rows ON COMMIT DROP AS
SELECT
    m.legacy_agent_id,
    m.legacy_name,
    m.position_agent_id,
    m.position_name,
    m.position_role,
    legacy.name AS observed_legacy_name,
    legacy.runtime_id AS legacy_runtime_id,
    legacy.model AS legacy_model,
    legacy.thinking_level AS legacy_thinking_level,
    position_agent.name AS observed_position_name,
    position_agent.role AS observed_position_role,
    tier_match.tier_id,
    tier_match.tier_name,
    tier_match.tier_members,
    CASE
        WHEN tier_match.has_exact_member
            THEN 'exact'
        WHEN tier_match.has_model_match THEN 'model-only'
        ELSE 'differs'
    END AS fidelity,
    tier_match.match_count
FROM m471_profile_map m
LEFT JOIN agent legacy
    ON legacy.id = m.legacy_agent_id
   AND legacy.workspace_id = :'workspace_id'::uuid
LEFT JOIN agent position_agent
    ON position_agent.id = m.position_agent_id
   AND position_agent.workspace_id = :'workspace_id'::uuid
LEFT JOIN LATERAL (
    SELECT
        (array_agg(t.id ORDER BY t.idx, t.created_at, t.id))[1] AS tier_id,
        (array_agg(t.name ORDER BY t.idx, t.created_at, t.id))[1] AS tier_name,
        jsonb_agg(jsonb_build_object(
            'position', tm.position,
            'runtime_id', tm.runtime_id,
            'model', tm.model,
            'thinking_level', tm.thinking_level,
            'enabled', tm.enabled
        ) ORDER BY tm.position) AS tier_members,
        bool_or(legacy.runtime_id = tm.runtime_id
            AND legacy.model = tm.model
            AND legacy.thinking_level IS NOT DISTINCT FROM tm.thinking_level)
            FILTER (WHERE tm.enabled) AS has_exact_member,
        bool_or(legacy.model = tm.model) FILTER (WHERE tm.enabled) AS has_model_match,
        (count(DISTINCT t.id) FILTER (WHERE tm.enabled))::integer AS match_count
    FROM model_tier t
    JOIN model_tier_member tm ON tm.tier_id = t.id
    WHERE t.workspace_id = :'workspace_id'::uuid
      AND m.position_role = ANY(t.roles)
      AND t.name = m.tier_name
) tier_match ON TRUE;

DO $$
DECLARE
    invalid_rows TEXT;
BEGIN
    SELECT string_agg(
        format('%s (%s): legacy=%s, position=%s/%s, role=%s, tier=%s, tier_matches=%s',
            legacy_name, legacy_agent_id, COALESCE(observed_legacy_name, '<missing>'),
            COALESCE(observed_position_name, '<missing>'), COALESCE(observed_position_role, '<null>'),
            position_role, tier_name, COALESCE(match_count::text, '0')),
        E'\n' ORDER BY legacy_name)
    INTO invalid_rows
    FROM m471_seed_rows
    WHERE observed_legacy_name IS DISTINCT FROM legacy_name
       OR observed_position_name IS DISTINCT FROM position_name
       OR observed_position_role IS DISTINCT FROM position_role
       OR match_count IS DISTINCT FROM 1;

    IF invalid_rows IS NOT NULL THEN
        RAISE EXCEPTION 'M-471 preflight failed; review live agent/tier mapping before seeding:%', E'\n' || invalid_rows;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM m471_positions p
        LEFT JOIN agent a ON a.id = p.position_agent_id
                         AND a.workspace_id = current_setting('m471.workspace_id')::uuid
        WHERE a.name IS DISTINCT FROM p.position_name
           OR a.role IS DISTINCT FROM p.position_role
    ) THEN
        SELECT string_agg(
            format('%s (%s): observed=%s/%s, expected role=%s',
                p.position_name, p.position_agent_id, COALESCE(a.name, '<missing>'),
                COALESCE(a.role, '<null>'), p.position_role),
            E'\n' ORDER BY p.position_name)
        INTO invalid_rows
        FROM m471_positions p
        LEFT JOIN agent a ON a.id = p.position_agent_id
                         AND a.workspace_id = current_setting('m471.workspace_id')::uuid
        WHERE a.name IS DISTINCT FROM p.position_name
           OR a.role IS DISTINCT FROM p.position_role;
        RAISE EXCEPTION 'M-471 canonical position preflight failed:%', E'\n' || invalid_rows;
    END IF;

    IF EXISTS (
        SELECT 1 FROM m471_seed_rows a
        JOIN m471_seed_rows b ON lower(a.legacy_name) = lower(b.legacy_name)
                              AND a.legacy_agent_id < b.legacy_agent_id
    ) THEN
        RAISE EXCEPTION 'M-471 preset names collide case-insensitively';
    END IF;

    IF EXISTS (
        SELECT 1 FROM m471_seed_rows s
        JOIN agent_preset p ON p.workspace_id = current_setting('m471.workspace_id')::uuid
                           AND p.legacy_agent_id = s.legacy_agent_id
        WHERE p.agent_id <> s.position_agent_id OR p.tier_id <> s.tier_id OR p.name <> s.legacy_name
    ) THEN
        RAISE EXCEPTION 'M-471 found a conflicting preset alias; refusing to overwrite it';
    END IF;

    IF EXISTS (
        SELECT 1 FROM m471_seed_rows s
        JOIN agent_preset p ON p.workspace_id = current_setting('m471.workspace_id')::uuid
                           AND lower(p.name) = lower(s.legacy_name)
        WHERE p.legacy_agent_id IS DISTINCT FROM s.legacy_agent_id
           OR p.agent_id <> s.position_agent_id
           OR p.tier_id <> s.tier_id
    ) THEN
        RAISE EXCEPTION 'M-471 found a conflicting preset name; refusing to overwrite it';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM m471_positions p
        LEFT JOIN agent a ON a.id = p.position_agent_id AND a.workspace_id = current_setting('m471.workspace_id')::uuid
        LEFT JOIN model_tier t ON t.workspace_id = current_setting('m471.workspace_id')::uuid AND p.position_role = ANY(t.roles)
        GROUP BY p.position_agent_id
        HAVING count(DISTINCT t.id) = 0
    ) THEN
        RAISE EXCEPTION 'M-471 found a position with no role tiers; refusing to enable tier routing';
    END IF;
END $$;

-- This result is the required pre-seed snapshot. Save and review it before the
-- second invocation with apply=true. No mutation occurs in the default mode.
SELECT legacy_agent_id, legacy_name, position_agent_id, position_name, position_role,
       legacy_runtime_id, legacy_model, legacy_thinking_level,
       tier_id, tier_name, tier_members, fidelity
FROM m471_seed_rows
ORDER BY position_name, legacy_name;

SELECT DISTINCT p.position_agent_id, p.position_name, p.position_role,
       t.id AS tier_id, t.name AS tier_name, t.idx,
       EXISTS (
           SELECT 1 FROM agent_tier at
           WHERE at.agent_id = p.position_agent_id AND at.tier_id = t.id
       ) AS already_assigned
FROM m471_positions p
JOIN model_tier t ON t.workspace_id = :'workspace_id'::uuid
                 AND p.position_role = ANY(t.roles)
ORDER BY p.position_name, t.idx, t.name;

-- M-520: preset seeding is FROZEN. The M-471 rollout already ran on lane z;
-- the resolver, legacy read aliases and existing preset rows stay untouched,
-- but no code path may add new preset rows. The apply branch is therefore a
-- documented no-op: the snapshot result sets above are the entire output.
-- Any future preset <-> Role conversion needs a separate, Architect-approved
-- and reviewed script (see docs/moct-x/agent-presets-seed.md).
\if :apply
    \echo 'M-520: preset seeding is FROZEN. No changes applied (no preset rows, agent_tier, use_tiers or workspace settings written). The snapshot above is a read-only view of the current lane state.'
\else
    \echo 'Snapshot only: no changes applied. Review the rows; apply mode is frozen per M-520.'
\endif

COMMIT;
