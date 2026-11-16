-- M-359: explicit-vs-auto marker on the task tier stamp.
--
-- An `@agent#tier` pick (or tier-picker/API override) stamps the named tier
-- with tier_explicit = TRUE; the plain `@agent` idx-ranked resolver leaves it
-- FALSE. The run record reads it to show which tier ran and whether the
-- sender pinned it or the resolver picked it. Defaults FALSE so every
-- pre-M-359 row reads as auto.
ALTER TABLE agent_task_queue
    ADD COLUMN tier_explicit BOOLEAN NOT NULL DEFAULT FALSE;
