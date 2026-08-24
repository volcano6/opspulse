-- 0002_restore_schema.sql: Restore execution records and audit tracking

CREATE TABLE IF NOT EXISTS restore_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_name TEXT NOT NULL,
    asset_id TEXT,
    snapshot_id TEXT NOT NULL,
    source_server TEXT NOT NULL,
    target_server TEXT NOT NULL,
    target_path TEXT,
    status TEXT NOT NULL,                  -- 'running', 'success', 'failed'
    files_restored INTEGER DEFAULT 0,
    total_bytes_restored INTEGER DEFAULT 0,
    duration_seconds REAL DEFAULT 0.0,
    error_message TEXT,
    log_path TEXT,
    started_at DATETIME NOT NULL,
    finished_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_restore_runs_job_name ON restore_runs(job_name);
CREATE INDEX IF NOT EXISTS idx_restore_runs_target_server ON restore_runs(target_server);
CREATE INDEX IF NOT EXISTS idx_restore_runs_started_at ON restore_runs(started_at DESC);
