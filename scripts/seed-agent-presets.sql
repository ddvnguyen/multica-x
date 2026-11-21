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
    position_role TEXT NOT NULL
) ON COMMIT DROP;

INSERT INTO m471_profile_map VALUES
    ('96a1cd5e-2a6e-4ced-a8b3-c28d413e956e', 'Architect', '96a1cd5e-2a6e-4ced-a8b3-c28d413e956e', 'Architect', 'architect'),
    ('bd7b7aae-88e9-4260-a7b5-301993757e59', 'Claude Architect', '96a1cd5e-2a6e-4ced-a8b3-c28d413e956e', 'Architect', 'architect'),
    ('7895c8ca-aed6-461e-9fea-a115b2681872', 'Opus Architect', '96a1cd5e-2a6e-4ced-a8b3-c28d413e956e', 'Architect', 'architect'),
    ('88908ce5-231a-450c-a2a1-5212e028a1eb', 'Codex | Architect', '96a1cd5e-2a6e-4ced-a8b3-c28d413e956e', 'Architect', 'architect'),
    ('2bd96ccd-0e7a-48cb-96d4-4e90f334b6fe', 'Lead', '2bd96ccd-0e7a-48cb-96d4-4e90f334b6fe', 'Lead', 'management'),
    ('9ecc1dde-1bc4-4f58-a9ef-1fcf59fb8545', 'Lead (Muse Spark)', '2bd96ccd-0e7a-48cb-96d4-4e90f334b6fe', 'Lead', 'management'),
    ('0e86b49e-1c6a-4a86-93a6-b602b9a86eae', 'Orchestrator', '0e86b49e-1c6a-4a86-93a6-b602b9a86eae', 'Orchestrator', 'management'),
    ('c0fb4a93-0990-482e-8871-ead35d7c6bbb', 'Orchestrator (Backup)', '0e86b49e-1c6a-4a86-93a6-b602b9a86eae', 'Orchestrator', 'management'),
    ('d5c380b6-590a-498f-8591-557c05f462ec', 'Software Engineer', 'd5c380b6-590a-498f-8591-557c05f462ec', 'Software Engineer', 'engineer'),
    ('1f23308e-83e4-4923-9ff6-2fc7ce80d841', '[Go Free] Engineer', 'd5c380b6-590a-498f-8591-557c05f462ec', 'Software Engineer', 'engineer'),
    ('a62593fd-09b4-4fe6-aae9-5351829e8a7d', '[Free] Codebuddy Engineer', 'a62593fd-09b4-4fe6-aae9-5351829e8a7d', '[Free] Codebuddy Engineer', 'engineer'),
    ('02cd4c29-7693-4d5f-a2dd-7260f4aa08dc', 'Codebuddy Senior Engineer', 'a62593fd-09b4-4fe6-aae9-5351829e8a7d', '[Free] Codebuddy Engineer', 'engineer');

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
        count(DISTINCT t.id)::integer AS match_count
    FROM model_tier t
    JOIN model_tier_member tm ON tm.tier_id = t.id
    WHERE t.workspace_id = :'workspace_id'::uuid
      AND m.position_role = ANY(t.roles)
      AND tm.runtime_id = legacy.runtime_id
      AND tm.model = legacy.model
      AND tm.thinking_level IS NOT DISTINCT FROM legacy.thinking_level
) tier_match ON TRUE;

DO $$
DECLARE
    invalid_rows TEXT;
BEGIN
    SELECT string_agg(
        format('%s (%s): legacy=%s, position=%s/%s, role=%s, tier_matches=%s',
            legacy_name, legacy_agent_id, COALESCE(observed_legacy_name, '<missing>'),
            COALESCE(observed_position_name, '<missing>'), COALESCE(observed_position_role, '<null>'),
            position_role, COALESCE(match_count::text, '0')),
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
        FROM (SELECT DISTINCT position_agent_id, position_role FROM m471_profile_map) p
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
       legacy_runtime_id, legacy_model, legacy_thinking_level, tier_id, tier_name
FROM m471_seed_rows
ORDER BY position_name, legacy_name;

SELECT DISTINCT p.position_agent_id, p.position_name, p.position_role,
       t.id AS tier_id, t.name AS tier_name, t.idx,
       EXISTS (
           SELECT 1 FROM agent_tier at
           WHERE at.agent_id = p.position_agent_id AND at.tier_id = t.id
       ) AS already_assigned
FROM m471_profile_map p
JOIN model_tier t ON t.workspace_id = :'workspace_id'::uuid
                 AND p.position_role = ANY(t.roles)
ORDER BY p.position_name, t.idx, t.name;

\if :apply
    INSERT INTO agent_preset (workspace_id, name, agent_id, tier_id, legacy_agent_id)
    SELECT :'workspace_id'::uuid, legacy_name, position_agent_id, tier_id, legacy_agent_id
    FROM m471_seed_rows
    ON CONFLICT (workspace_id, (lower(name))) DO NOTHING;

    INSERT INTO agent_tier (agent_id, tier_id)
    SELECT DISTINCT p.position_agent_id, t.id
    FROM m471_profile_map p
    JOIN model_tier t ON t.workspace_id = :'workspace_id'::uuid
                     AND p.position_role = ANY(t.roles)
    ON CONFLICT DO NOTHING;

    UPDATE agent a
    SET use_tiers = TRUE, updated_at = now()
    FROM (SELECT DISTINCT position_agent_id FROM m471_profile_map) p
    WHERE a.id = p.position_agent_id
      AND a.workspace_id = :'workspace_id'::uuid
      AND a.use_tiers IS DISTINCT FROM TRUE;

    UPDATE workspace
    SET settings = COALESCE(settings, '{}'::jsonb) || '{"agent_presets_enabled":true}'::jsonb,
        updated_at = now()
    WHERE id = :'workspace_id'::uuid
      AND settings->'agent_presets_enabled' IS DISTINCT FROM 'true'::jsonb;

    \echo 'M-471 seed applied. Rollback switch: set agent_presets_enabled=false for this workspace.'
\else
    \echo 'Snapshot only: no changes applied. Review the rows, then re-run with -v apply=true.'
\endif

COMMIT;
