package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// RestoreRun represents a single restore operation execution record.
type RestoreRun struct {
	ID                 int64      `json:"id"`
	JobName            string     `json:"job_name"`
	AssetID            string     `json:"asset_id,omitempty"`
	SnapshotID         string     `json:"snapshot_id"`
	SourceServer       string     `json:"source_server"`
	TargetServer       string     `json:"target_server"`
	TargetPath         string     `json:"target_path,omitempty"`
	Status             string     `json:"status"` // 'running', 'success', 'failed'
	FilesRestored      int64      `json:"files_restored"`
	TotalBytesRestored int64      `json:"total_bytes_restored"`
	DurationSeconds    float64    `json:"duration_seconds"`
	ErrorMessage       string     `json:"error_message,omitempty"`
	LogPath            string     `json:"log_path,omitempty"`
	StartedAt          time.Time  `json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at,omitempty"`
}

// RestoreRepo handles CRUD operations for the restore_runs table.
type RestoreRepo struct {
	db *DB
}

// NewRestoreRepo creates a new RestoreRepo.
func NewRestoreRepo(db *DB) *RestoreRepo {
	return &RestoreRepo{db: db}
}

// CreateRun inserts a new restore run record and sets its generated ID.
func (r *RestoreRepo) CreateRun(ctx context.Context, run *RestoreRun) (int64, error) {
	query := `
	INSERT INTO restore_runs (
		job_name, asset_id, snapshot_id, source_server, target_server, target_path,
		status, files_restored, total_bytes_restored, duration_seconds, error_message,
		log_path, started_at, finished_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	var finishedAtStr sql.NullString
	if run.FinishedAt != nil {
		finishedAtStr = sql.NullString{String: run.FinishedAt.UTC().Format(time.RFC3339), Valid: true}
	}

	res, err := r.db.Conn().ExecContext(ctx, query,
		run.JobName,
		nullIfEmpty(run.AssetID),
		run.SnapshotID,
		run.SourceServer,
		run.TargetServer,
		nullIfEmpty(run.TargetPath),
		run.Status,
		run.FilesRestored,
		run.TotalBytesRestored,
		run.DurationSeconds,
		nullIfEmpty(run.ErrorMessage),
		nullIfEmpty(run.LogPath),
		run.StartedAt.UTC().Format(time.RFC3339),
		finishedAtStr,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert restore_run: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get last insert id: %w", err)
	}

	run.ID = id
	return id, nil
}

// UpdateRun updates metrics and completion state for an existing restore run.
func (r *RestoreRepo) UpdateRun(ctx context.Context, run *RestoreRun) error {
	query := `
	UPDATE restore_runs SET
		status = ?,
		files_restored = ?,
		total_bytes_restored = ?,
		duration_seconds = ?,
		error_message = ?,
		log_path = ?,
		finished_at = ?
	WHERE id = ?`

	var finishedAtStr sql.NullString
	if run.FinishedAt != nil {
		finishedAtStr = sql.NullString{String: run.FinishedAt.UTC().Format(time.RFC3339), Valid: true}
	}

	_, err := r.db.Conn().ExecContext(ctx, query,
		run.Status,
		run.FilesRestored,
		run.TotalBytesRestored,
		run.DurationSeconds,
		nullIfEmpty(run.ErrorMessage),
		nullIfEmpty(run.LogPath),
		finishedAtStr,
		run.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update restore_run (id: %d): %w", run.ID, err)
	}

	return nil
}

// ListRuns retrieves historic restore runs, optionally filtered by jobName.
func (r *RestoreRepo) ListRuns(ctx context.Context, jobName string, limit int) ([]RestoreRun, error) {
	if limit <= 0 {
		limit = 20
	}

	var query string
	var args []any

	if jobName != "" {
		query = `
		SELECT id, job_name, asset_id, snapshot_id, source_server, target_server, target_path,
		       status, files_restored, total_bytes_restored, duration_seconds, error_message,
		       log_path, started_at, finished_at
		FROM restore_runs
		WHERE job_name = ?
		ORDER BY started_at DESC
		LIMIT ?`
		args = []any{jobName, limit}
	} else {
		query = `
		SELECT id, job_name, asset_id, snapshot_id, source_server, target_server, target_path,
		       status, files_restored, total_bytes_restored, duration_seconds, error_message,
		       log_path, started_at, finished_at
		FROM restore_runs
		ORDER BY started_at DESC
		LIMIT ?`
		args = []any{limit}
	}

	rows, err := r.db.Conn().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list restore_runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var runs []RestoreRun
	for rows.Next() {
		var run RestoreRun
		var assetID, targetPath, errMsg, logPath, startedStr, finishedStr sql.NullString

		err := rows.Scan(
			&run.ID,
			&run.JobName,
			&assetID,
			&run.SnapshotID,
			&run.SourceServer,
			&run.TargetServer,
			&targetPath,
			&run.Status,
			&run.FilesRestored,
			&run.TotalBytesRestored,
			&run.DurationSeconds,
			&errMsg,
			&logPath,
			&startedStr,
			&finishedStr,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan restore_run row: %w", err)
		}

		if assetID.Valid {
			run.AssetID = assetID.String
		}
		if targetPath.Valid {
			run.TargetPath = targetPath.String
		}
		if errMsg.Valid {
			run.ErrorMessage = errMsg.String
		}
		if logPath.Valid {
			run.LogPath = logPath.String
		}
		if startedStr.Valid {
			run.StartedAt, _ = time.Parse(time.RFC3339, startedStr.String)
		}
		if finishedStr.Valid {
			t, _ := time.Parse(time.RFC3339, finishedStr.String)
			run.FinishedAt = &t
		}

		runs = append(runs, run)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return runs, nil
}

// GetLatestRun returns the most recent restore execution record for a given jobName.
func (r *RestoreRepo) GetLatestRun(ctx context.Context, jobName string) (*RestoreRun, error) {
	runs, err := r.ListRuns(ctx, jobName, 1)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return &runs[0], nil
}
