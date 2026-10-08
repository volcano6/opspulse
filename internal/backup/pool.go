package backup

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/volcano6/opspulse/internal/format"
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
//
// Function-level semantics: maxParallel <= 0 means "no limit" (every job runs at
// once). This differs from the CLI convention, where 0 is the default (5) and
// "unlimited" is explicit -- that mapping happens in cmd/opspulse via
// cliutil.ParseParallelism, so callers must pass either a positive limit or
// cliutil.Unlimited (negative), never a raw 0.
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
	// UntrackedCount counts runs whose audit record could not be created
	// (--allow-untracked emergency mode), whatever their outcome. They are kept
	// apart from the plain success/failure tallies because the CLI must never
	// report an untracked run as an ordinary one.
	UntrackedCount int
	IsDryRun       bool
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

	// Concurrency limiter channel. maxParallel <= 0 is the function-level
	// "unlimited" (see NewPool); the CLI maps its own "unlimited" onto it.
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
			if runRecord == nil {
				errMsg := ""
				if err != nil {
					errMsg = err.Error()
				}
				runRecord = &storage.BackupRun{
					JobName:      job.Name,
					ServerName:   job.Server,
					Status:       "failed",
					ErrorMessage: errMsg,
					StartedAt:    time.Now(),
				}
			}
			res.Runs[index] = runRecord
			switch {
			case runRecord.Untracked:
				// The run happened; only its audit record is missing. Count it by
				// the outcome it really had and let the untracked tally carry the
				// gap: an untracked success must not be reported as a failed
				// backup, and an untracked failure is still a failure.
				res.UntrackedCount++
				if runRecord.Status == "success" {
					res.SuccessCount++
				} else {
					res.FailureCount++
				}
			case err == nil && runRecord.Status == "success":
				res.SuccessCount++
			default:
				res.FailureCount++
			}
			mu.Unlock()
		}(i, j)
	}

	wg.Wait()
	res.TotalDuration = time.Since(startTime)
	return res, nil
}

// RunStatusLabel renders a run's status for the batch summary and the
// status/history tables. An untracked run is flagged wherever its status is
// shown, so an audit gap is never read as an ordinary result - whatever the
// outcome.
func RunStatusLabel(r storage.BackupRun) string {
	label := strings.ToUpper(r.Status)
	if r.Untracked {
		label += " (UNTRACKED)"
	}
	return label
}

// PrintSummary writes a formatted table of batch backup execution results.
func (p *PoolResult) PrintSummary(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "\n==================== Backup Execution Summary ====================")
	_, _ = fmt.Fprintln(tw, "JOB\tSERVER\tSTATUS\tSNAPSHOT\tADDED\tTOTAL\tDURATION\tLOG FILE")
	_, _ = fmt.Fprintln(tw, "---\t------\t------\t--------\t-----\t-----\t--------\t--------")

	var failedJobs []*storage.BackupRun
	for _, r := range p.Runs {
		if r == nil {
			continue
		}

		status := RunStatusLabel(*r)
		if p.IsDryRun {
			status = "DRY-RUN"
		} else if r.Status == "failed" {
			failedJobs = append(failedJobs, r)
		}

		snapID := r.SnapshotID
		if snapID == "" {
			snapID = "-"
		} else if len(snapID) > 8 {
			snapID = snapID[:8]
		}

		addedStr := format.Bytes(r.DataAddedBytes)
		totalStr := format.Bytes(r.TotalBytes)
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
	if p.UntrackedCount > 0 {
		summaryLine += fmt.Sprintf(" | Untracked: %d", p.UntrackedCount)
	}
	if p.IsDryRun {
		summaryLine += " (Dry Run)"
	}
	_, _ = fmt.Fprintln(w, summaryLine)

	if len(failedJobs) > 0 {
		_, _ = fmt.Fprintln(w, "\nFailures:")
		for _, f := range failedJobs {
			errMsg := f.ErrorMessage
			if errMsg == "" {
				errMsg = "unknown error"
			}
			_, _ = fmt.Fprintf(w, "  - %s (%s): %s\n", f.JobName, f.ServerName, errMsg)
		}
	}
	_, _ = fmt.Fprintln(w, "==================================================================")
}
