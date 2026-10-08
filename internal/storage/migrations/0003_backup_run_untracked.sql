-- 0003_backup_run_untracked.sql: mark backup runs whose audit record could not be created.
--
-- The initial 'running' row is written before any remote work so that a broken
-- audit store fails the run closed. In the explicit --allow-untracked emergency
-- mode the run proceeds anyway and every persisted record carries untracked = 1,
-- so history, status and notifications can mark the audit gap.
ALTER TABLE backup_runs ADD COLUMN untracked INTEGER NOT NULL DEFAULT 0;
