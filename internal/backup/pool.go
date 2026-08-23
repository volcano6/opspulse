package backup

import (
	"context"
	"fmt"
	"io"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/volcano6/opspulse/internal/storage"
)

type syncWriter struct {
	w  io.Writer
	mu sync.Mutex
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// Pool coordinates concurrent or sequential execution of multiple backup jobs.
type Pool struct {
	runner      *Runner
	maxParallel int
}

// NewPool creates a new backup Pool with the specified concurrency limit.
// If maxParallel <= 0, jobs run concurrently without limit.
func NewPool(runner *Runner, maxParallel int) *Pool {
	return &Pool{
		runner:      runner,
		maxParallel: maxParallel,
	}
}

// PoolResult aggregates the outcome of running a batch of backup jobs.
type PoolResult struct {
	Runs          []*storage.BackupRun
	TotalDuration time.Duration
	SuccessCount  int
	FailureCount  int
	IsDryRun      bool
}

// RunAll executes a slice of backup jobs respecting the concurrency limit.
func (p *Pool) RunAll(ctx context.Context, jobs []Job, dryRun bool, out io.Writer) (*PoolResult, error) {
	if len(jobs) == 0 {
		return &PoolResult{}, nil
	}

	if out == nil {
		out = io.Discard
	}
	safeOut := &syncWriter{w: out}

	startTime := time.Now()
	res := &PoolResult{
		Runs:     make([]*storage.BackupRun, len(jobs)),
		IsDryRun: dryRun,
	}

	if dryRun {
		_, _ = fmt.Fprintln(safeOut, "\n========== [DRY-RUN] Simulating Backup Jobs ==========")
		for i, job := range jobs {
			script, err := BuildBackupScript(job)
			duration := 10 * time.Millisecond
			if err != nil {
				res.Runs[i] = &storage.BackupRun{
					JobName:      job.Name,
					ServerName:   job.Server,
					Status:       "failed",
					ErrorMessage: err.Error(),
					StartedAt:    time.Now(),
				}
				res.FailureCount++
			} else {
				_, _ = fmt.Fprintf(safeOut, "\n--> [DRY-RUN] Job %q on %s (%d bytes script)\n",
					job.Name, job.Server, len(script))
				res.Runs[i] = &storage.BackupRun{
					JobName:         job.Name,
					ServerName:      job.Server,
					Status:          "dry-run",
					SnapshotID:      "dry-run",
					DurationSeconds: duration.Seconds(),
					StartedAt:       time.Now(),
				}
				res.SuccessCount++
			}
		}
		res.TotalDuration = time.Since(startTime)
		return res, nil
	}

	// Concurrency limiter channel
	limit := p.maxParallel
	if limit <= 0 || limit > len(jobs) {
		limit = len(jobs)
	}

	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, j := range jobs {
		wg.Add(1)
		go func(index int, job Job) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			runRecord, err := p.runner.Run(ctx, job, safeOut)

			mu.Lock()
			res.Runs[index] = runRecord
			if err == nil && runRecord != nil && runRecord.Status == "success" {
				res.SuccessCount++
			} else {
				res.FailureCount++
			}
			mu.Unlock()
		}(i, j)
	}

	wg.Wait()
	res.TotalDuration = time.Since(startTime)
	return res, nil
}

// PrintSummary writes a formatted table of batch backup execution results.
func (p *PoolResult) PrintSummary(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "\n==================== Backup Execution Summary ====================")
	_, _ = fmt.Fprintln(tw, "JOB\tSERVER\tSTATUS\tSNAPSHOT\tADDED\tTOTAL\tDURATION\tLOG FILE")
	_, _ = fmt.Fprintln(tw, "---\t------\t------\t--------\t-----\t-----\t--------\t--------")

	for _, r := range p.Runs {
		if r == nil {
			continue
		}

		status := r.Status
		if p.IsDryRun {
			status = "DRY-RUN"
		} else if status == "success" {
			status = "SUCCESS"
		} else if status == "failed" {
			status = "FAILED"
		}

		snapID := r.SnapshotID
		if snapID == "" {
			snapID = "-"
		} else if len(snapID) > 8 {
			snapID = snapID[:8]
		}

		addedStr := FormatBytes(r.DataAddedBytes)
		totalStr := FormatBytes(r.TotalBytes)
		durationStr := fmt.Sprintf("%.2fs", r.DurationSeconds)
		logPath := r.LogPath
		if logPath == "" {
			logPath = "-"
		}

		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.JobName,
			r.ServerName,
			status,
			snapID,
			addedStr,
			totalStr,
			durationStr,
			logPath,
		)
	}
	_ = tw.Flush()

	_, _ = fmt.Fprintln(w, "------------------------------------------------------------------")
	summaryLine := fmt.Sprintf("Total Jobs: %d | Succeeded: %d | Failed: %d | Total Duration: %.2fs",
		len(p.Runs), p.SuccessCount, p.FailureCount, p.TotalDuration.Seconds())
	if p.IsDryRun {
		summaryLine += " (Dry Run)"
	}
	_, _ = fmt.Fprintln(w, summaryLine)
	_, _ = fmt.Fprintln(w, "==================================================================")
}

// FormatBytes converts a byte count into a human-readable string (e.g. 10.5 MB).
func FormatBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	return fmt.Sprintf("%.2f %s", float64(b)/float64(div), units[exp])
}
