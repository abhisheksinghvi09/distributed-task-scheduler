-- Run only after no code path reads the legacy `command` column.
ALTER TABLE tasks DROP COLUMN IF EXISTS command;
