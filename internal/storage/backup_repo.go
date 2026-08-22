package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// BackupRun represents a recorded execution run of a backup job.
type BackupRun struct {
	ID              int64      `json:"id"`
	JobName         string     `json:"job_name"`
	ServerName      string     `json:"server_name"`
	SnapshotID      string     `json:"snapshot_id,omitempty"`
	Status          string     `json:"status"` // "success", "failed", "running"
	FilesNew        int64      `json:"files_new"`
	FilesChanged    int64      `json:"files_changed"`
	FilesUnmodified int64      `json:"files_unmodified"`
	DataAddedBytes  int64      `json:"data_added_bytes"`
	TotalBytes      int64      `json:"total_bytes"`
	DurationSeconds float64    `json:"duration_seconds"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	LogPath         string     `json:"log_path,omitempty"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
}

// BackupRepo manages CRUD operations for backup runs in SQLite.
type BackupRepo struct {
	db *DB
}

// NewBackupRepo creates a new BackupRepo repository.
func NewBackupRepo(db *DB) *BackupRepo {
	return &BackupRepo{db: db}
}

// CreateRun inserts a new backup execution run record into the database.
func (r *BackupRepo) CreateRun(ctx context.Context, run *BackupRun) (int64, error) {
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}

	query := `
	INSERT INTO backup_runs (
		job_name, server_name, snapshot_id, status,
		files_new, files_changed, files_unmodified,
		data_added_bytes, total_bytes, duration_seconds,
		error_message, log_path, started_at, finished_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	var finishedAtVal *string
	if run.FinishedAt != nil {
		formatted := run.FinishedAt.Format(time.RFC3339)
		finishedAtVal = &formatted
	}

	res, err := r.db.Conn().ExecContext(ctx, query,
		run.JobName,
		run.ServerName,
		run.SnapshotID,
		run.Status,
		run.FilesNew,
		run.FilesChanged,
		run.FilesUnmodified,
		run.DataAddedBytes,
		run.TotalBytes,
		run.DurationSeconds,
		run.ErrorMessage,
		run.LogPath,
		run.StartedAt.Format(time.RFC3339),
		finishedAtVal,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert backup_run: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get last insert id: %w", err)
	}
	run.ID = id
	return id, nil
}

// UpdateRun updates an existing backup execution run record.
func (r *BackupRepo) UpdateRun(ctx context.Context, run *BackupRun) error {
	query := `
	UPDATE backup_runs SET
		snapshot_id = ?,
		status = ?,
		files_new = ?,
		files_changed = ?,
		files_unmodified = ?,
		data_added_bytes = ?,
		total_bytes = ?,
		duration_seconds = ?,
		error_message = ?,
		log_path = ?,
		finished_at = ?
	WHERE id = ?`

	var finishedAtVal *string
	if run.FinishedAt != nil {
		formatted := run.FinishedAt.Format(time.RFC3339)
		finishedAtVal = &formatted
	}

	_, err := r.db.Conn().ExecContext(ctx, query,
		run.SnapshotID,
		run.Status,
		run.FilesNew,
		run.FilesChanged,
		run.FilesUnmodified,
		run.DataAddedBytes,
		run.TotalBytes,
		run.DurationSeconds,
		run.ErrorMessage,
		run.LogPath,
		finishedAtVal,
		run.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update backup_run %d: %w", run.ID, err)
	}
	return nil
}

// ListRuns returns historical backup execution runs for a specific job, ordered by started_at DESC.
func (r *BackupRepo) ListRuns(ctx context.Context, jobName string, limit int) ([]BackupRun, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
	SELECT
		id, job_name, server_name, snapshot_id, status,
		files_new, files_changed, files_unmodified,
		data_added_bytes, total_bytes, duration_seconds,
		error_message, log_path, started_at, finished_at
	FROM backup_runs
	WHERE (? = '' OR job_name = ?)
	ORDER BY started_at DESC
	LIMIT ?`

	rows, err := r.db.Conn().QueryContext(ctx, query, jobName, jobName, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query backup_runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []BackupRun
	for rows.Next() {
		run, err := scanBackupRun(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan backup_run row: %w", err)
		}
		results = append(results, *run)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return results, nil
}

// GetLatestRun retrieves the most recent backup execution run for a specific job.
func (r *BackupRepo) GetLatestRun(ctx context.Context, jobName string) (*BackupRun, error) {
	query := `
	SELECT
		id, job_name, server_name, snapshot_id, status,
		files_new, files_changed, files_unmodified,
		data_added_bytes, total_bytes, duration_seconds,
		error_message, log_path, started_at, finished_at
	FROM backup_runs
	WHERE job_name = ?
	ORDER BY started_at DESC
	LIMIT 1`

	row := r.db.Conn().QueryRowContext(ctx, query, jobName)
	run, err := scanBackupRun(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // No runs yet
		}
		return nil, fmt.Errorf("failed to query latest backup_run for %q: %w", jobName, err)
	}

	return run, nil
}

// GetAllLatestRuns retrieves the most recent backup run for every known job.
func (r *BackupRepo) GetAllLatestRuns(ctx context.Context) ([]BackupRun, error) {
	query := `
	SELECT
		id, job_name, server_name, snapshot_id, status,
		files_new, files_changed, files_unmodified,
		data_added_bytes, total_bytes, duration_seconds,
		error_message, log_path, started_at, finished_at
	FROM backup_runs
	WHERE id IN (
		SELECT MAX(id) FROM backup_runs GROUP BY job_name
	)
	ORDER BY job_name ASC`

	rows, err := r.db.Conn().QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query all latest backup_runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []BackupRun
	for rows.Next() {
		run, err := scanBackupRun(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan backup_run row: %w", err)
		}
		results = append(results, *run)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return results, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanBackupRun(s scannable) (*BackupRun, error) {
	var run BackupRun
	var snapshotID, errorMsg, logPath sql.NullString
	var startedAtStr string
	var finishedAtStr sql.NullString

	err := s.Scan(
		&run.ID,
		&run.JobName,
		&run.ServerName,
		&snapshotID,
		&run.Status,
		&run.FilesNew,
		&run.FilesChanged,
		&run.FilesUnmodified,
		&run.DataAddedBytes,
		&run.TotalBytes,
		&run.DurationSeconds,
		&errorMsg,
		&logPath,
		&startedAtStr,
		&finishedAtStr,
	)
	if err != nil {
		return nil, err
	}

	if snapshotID.Valid {
		run.SnapshotID = snapshotID.String
	}
	if errorMsg.Valid {
		run.ErrorMessage = errorMsg.String
	}
	if logPath.Valid {
		run.LogPath = logPath.String
	}

	// Parse timestamps
	if t, err := time.Parse(time.RFC3339, startedAtStr); err == nil {
		run.StartedAt = t
	} else if t, err := time.Parse("2006-01-02 15:04:05", startedAtStr); err == nil {
		run.StartedAt = t
	}

	if finishedAtStr.Valid {
		if t, err := time.Parse(time.RFC3339, finishedAtStr.String); err == nil {
			run.FinishedAt = &t
		} else if t, err := time.Parse("2006-01-02 15:04:05", finishedAtStr.String); err == nil {
			run.FinishedAt = &t
		}
	}

	return &run, nil
}
