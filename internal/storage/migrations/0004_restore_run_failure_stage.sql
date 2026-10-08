-- 0004_restore_run_failure_stage.sql: record which phase a restore failed in.
--
-- Pre-flight validation (job, secret, snapshot, target server, asset) runs
-- before any remote work. Those failures used to return before the restore_runs
-- row was created, so `ops restore history` silently omitted every attempt that
-- never reached restic. The row is now created first and carries the phase that
-- failed: 'validation', 'execution' or 'post_restore'. It stays '' for runs that
-- did not fail (running, success, dry-run).
ALTER TABLE restore_runs ADD COLUMN failure_stage TEXT NOT NULL DEFAULT '';
