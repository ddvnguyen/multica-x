-- M-348: the revision a passage comment was made on.
--
-- Comments fence on base_revision at write time but never stored it, so the
-- plan page could not say which revision a thread belongs to. Nullable: rows
-- predating this migration get no badge instead of a invented one.
ALTER TABLE agent_plan_comment ADD COLUMN revision INT;
