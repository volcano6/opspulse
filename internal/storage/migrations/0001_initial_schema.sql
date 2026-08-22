-- 0001_initial_schema.sql: Initial schema for OpsPulse SQLite storage

CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS backup_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_name TEXT NOT NULL,
    server_name TEXT NOT NULL,
    snapshot_id TEXT,
    status TEXT NOT NULL,
    files_new INTEGER NOT NULL DEFAULT 0,
    files_changed INTEGER NOT NULL DEFAULT 0,
    files_unmodified INTEGER NOT NULL DEFAULT 0,
    data_added_bytes INTEGER NOT NULL DEFAULT 0,
    total_bytes INTEGER NOT NULL DEFAULT 0,
    duration_seconds REAL NOT NULL DEFAULT 0.0,
    error_message TEXT,
    log_path TEXT,
    started_at DATETIME NOT NULL,
    finished_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_backup_runs_job_name ON backup_runs(job_name);
CREATE INDEX IF NOT EXISTS idx_backup_runs_started_at ON backup_runs(started_at);
