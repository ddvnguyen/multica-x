-- M-497 down: restore the fixed role vocabularies (fails when custom role
-- keys are in use; remove those rows first, then roll back).
ALTER TABLE model_tier
    ADD CONSTRAINT model_tier_roles_check
    CHECK (roles <@ ARRAY['architect','management','engineer','leader','reviewer','orchestrator','planning']::text[]);
ALTER TABLE agent
    ADD CONSTRAINT agent_role_check
    CHECK (role IS NULL OR role IN ('architect','management','engineer','leader','reviewer','orchestrator','planning'));
