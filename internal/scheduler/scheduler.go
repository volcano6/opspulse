// Package scheduler orchestrates cron-based automated backup job execution and alert notifications.
package scheduler

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/notify"
)

// ScheduledJob describes a registered backup job and its next execution timing.
type ScheduledJob struct {
	JobName  string       `json:"job_name"`
	Server   string       `json:"server"`
	Schedule string       `json:"schedule"`
	EntryID  cron.EntryID `json:"entry_id"`
	Next     time.Time    `json:"next"`
	Prev     time.Time    `json:"prev"`
}

// Scheduler coordinates cron scheduling of configured backup jobs.
type Scheduler struct {
	cron       *cron.Cron
	runner     *backup.Runner
	store      *backup.Store
	dispatcher *notify.Dispatcher
	out        io.Writer
	mu         sync.Mutex
	jobs       map[cron.EntryID]backup.Job
}

// New creates a new Scheduler instance.
func New(store *backup.Store, runner *backup.Runner, dispatcher *notify.Dispatcher, out io.Writer) *Scheduler {
	if out == nil {
		out = os.Stdout
	}

	cronLogger := cron.PrintfLogger(log.New(out, "[scheduler] ", log.LstdFlags))
	c := cron.New(
		cron.WithChain(
			cron.SkipIfStillRunning(cronLogger),
			cron.Recover(cronLogger),
		),
	)

	return &Scheduler{
		cron:       c,
		runner:     runner,
		store:      store,
		dispatcher: dispatcher,
		out:        out,
		jobs:       make(map[cron.EntryID]backup.Job),
	}
}

// ValidateSchedule verifies that the given cron expression is valid.
func ValidateSchedule(spec string) error {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return fmt.Errorf("schedule expression cannot be empty")
	}
	_, err := cron.ParseStandard(trimmed)
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", spec, err)
	}
	return nil
}

// RegisterJobs reads all jobs from the backup store and registers those with a non-empty Schedule.
func (s *Scheduler) RegisterJobs() ([]ScheduledJob, error) {
	if s.store == nil {
		return nil, fmt.Errorf("backup store is nil")
	}

	allJobs, err := s.store.List()
	if err != nil {
		return nil, fmt.Errorf("failed to list backup jobs: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear previously registered entries if any
	for id := range s.jobs {
		s.cron.Remove(id)
	}
	s.jobs = make(map[cron.EntryID]backup.Job)

	var registered []ScheduledJob

	for _, j := range allJobs {
		scheduleSpec := strings.TrimSpace(j.Schedule)
		if scheduleSpec == "" {
			continue
		}

		if err := ValidateSchedule(scheduleSpec); err != nil {
			return nil, fmt.Errorf("failed to validate schedule for job %q: %w", j.Name, err)
		}

		jobCopy := j
		entryID, err := s.cron.AddFunc(scheduleSpec, func() {
			s.executeJob(jobCopy)
		})
		if err != nil {
			return nil, fmt.Errorf("failed to schedule job %q with spec %q: %w", j.Name, scheduleSpec, err)
		}

		s.jobs[entryID] = jobCopy

		entry := s.cron.Entry(entryID)
		nextTime := entry.Next
		if nextTime.IsZero() && entry.Schedule != nil {
			nextTime = entry.Schedule.Next(time.Now())
		}
		registered = append(registered, ScheduledJob{
			JobName:  j.Name,
			Server:   j.Server,
			Schedule: scheduleSpec,
			EntryID:  entryID,
			Next:     nextTime,
			Prev:     entry.Prev,
		})
	}

	return registered, nil
}

func (s *Scheduler) executeJob(job backup.Job) {
	startTime := time.Now()
	_, _ = fmt.Fprintf(s.out, "[scheduler] >>> Triggering scheduled backup job %q (%s) at %s\n",
		job.Name, job.Server, startTime.Format("2006-01-02 15:04:05"))

	ctx := context.Background()
	var runRecordErr error
	var runRecordStatus = "failed"
	var snapshotID string
	var durationSec float64
	var errMsg string

	if s.runner != nil {
		rec, err := s.runner.Run(ctx, job, s.out)
		if rec != nil {
			runRecordStatus = rec.Status
			snapshotID = rec.SnapshotID
			durationSec = rec.DurationSeconds
			errMsg = rec.ErrorMessage
		}
		if err != nil {
			runRecordErr = err
			if errMsg == "" {
				errMsg = err.Error()
			}
		}
	} else {
		runRecordErr = fmt.Errorf("runner is nil")
		errMsg = "runner is not initialized"
	}

	// Dispatch notification if dispatcher is available
	if s.dispatcher != nil {
		event := notify.Event{
			JobName:         job.Name,
			Status:          runRecordStatus,
			Server:          job.Server,
			Snapshot:        snapshotID,
			DurationSeconds: durationSec,
			Error:           errMsg,
			Timestamp:       time.Now(),
		}
		if notifyErrs := s.dispatcher.Dispatch(ctx, event); len(notifyErrs) > 0 {
			for _, ne := range notifyErrs {
				_, _ = fmt.Fprintf(s.out, "[scheduler] Warning: notification delivery failed: %v\n", ne)
			}
		}
	}

	if runRecordErr != nil {
		_, _ = fmt.Fprintf(s.out, "[scheduler] <<< Scheduled job %q finished with error: %v\n", job.Name, runRecordErr)
	} else {
		_, _ = fmt.Fprintf(s.out, "[scheduler] <<< Scheduled job %q finished successfully (status: %s, duration: %.2fs)\n",
			job.Name, runRecordStatus, durationSec)
	}
}

// ListRegistered returns all currently registered scheduled jobs with updated execution times.
func (s *Scheduler) ListRegistered() []ScheduledJob {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result []ScheduledJob
	for entryID, job := range s.jobs {
		entry := s.cron.Entry(entryID)
		nextTime := entry.Next
		if nextTime.IsZero() && entry.Schedule != nil {
			nextTime = entry.Schedule.Next(time.Now())
		}
		result = append(result, ScheduledJob{
			JobName:  job.Name,
			Server:   job.Server,
			Schedule: job.Schedule,
			EntryID:  entryID,
			Next:     nextTime,
			Prev:     entry.Prev,
		})
	}
	return result
}

// Start begins the cron scheduler in the background.
func (s *Scheduler) Start() {
	s.cron.Start()
}

// Stop stops the scheduler and returns a context that finishes when all running jobs complete.
func (s *Scheduler) Stop() context.Context {
	return s.cron.Stop()
}

// Run blocks until the provided context is canceled, handling graceful shutdown.
func (s *Scheduler) Run(ctx context.Context) error {
	registered, err := s.RegisterJobs()
	if err != nil {
		return err
	}

	if len(registered) == 0 {
		_, _ = fmt.Fprintln(s.out, "[scheduler] Warning: no backup jobs configured with a schedule in backups.yaml.")
	} else {
		_, _ = fmt.Fprintf(s.out, "[scheduler] 📋 Registered %d scheduled backup job(s):\n", len(registered))
		for _, r := range registered {
			_, _ = fmt.Fprintf(s.out, "  - %-16s [%s] next: %s\n",
				r.JobName, r.Schedule, r.Next.Local().Format("2006-01-02 15:04:05"))
		}
	}

	s.Start()
	_, _ = fmt.Fprintln(s.out, "[scheduler] 🚀 Daemon started. Waiting for schedule triggers (press Ctrl+C to stop)...")

	<-ctx.Done()

	_, _ = fmt.Fprintln(s.out, "\n[scheduler] 🛑 Shutdown signal received. Waiting for active jobs to complete...")
	stopCtx := s.Stop()

	select {
	case <-stopCtx.Done():
		_, _ = fmt.Fprintln(s.out, "[scheduler] ✅ Scheduler stopped gracefully.")
	case <-time.After(30 * time.Second):
		_, _ = fmt.Fprintln(s.out, "[scheduler] ⚠️ Graceful shutdown timed out after 30s.")
	}

	return nil
}

// RunOnce executes all scheduled jobs once immediately without waiting for their cron triggers.
func (s *Scheduler) RunOnce(ctx context.Context) error {
	if s.store == nil {
		return fmt.Errorf("backup store is nil")
	}

	allJobs, err := s.store.List()
	if err != nil {
		return fmt.Errorf("failed to list backup jobs: %w", err)
	}

	var scheduledJobs []backup.Job
	for _, j := range allJobs {
		if strings.TrimSpace(j.Schedule) != "" {
			scheduledJobs = append(scheduledJobs, j)
		}
	}

	if len(scheduledJobs) == 0 {
		_, _ = fmt.Fprintln(s.out, "[scheduler] No scheduled backup jobs found.")
		return nil
	}

	_, _ = fmt.Fprintf(s.out, "[scheduler] Executing %d scheduled job(s) sequentially (--once mode)...\n", len(scheduledJobs))

	var firstErr error
	for _, job := range scheduledJobs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			s.executeJob(job)
		}
	}

	return firstErr
}
