// Package scheduler orchestrates cron-based automated backup job execution and alert notifications.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/notify"
	"github.com/volcano6/opspulse/internal/storage"
)

const (
	// notifyWorkers is the fixed number of goroutines that deliver queued
	// notifications. It is deliberately small: notifications are best-effort
	// and must never compete with backup execution for resources.
	notifyWorkers = 2
	// notifyQueueSize bounds pending notifications. When it fills up, new
	// notifications are dropped with a warning instead of blocking the caller
	// or growing without bound.
	notifyQueueSize = 64
	// notifyDeliveryTimeout bounds one dispatch attempt chain so a wedged
	// webhook cannot hold a worker forever.
	notifyDeliveryTimeout = 30 * time.Second
	// dispatcherDrainTimeout bounds how long shutdown waits for queued
	// notifications to be delivered.
	dispatcherDrainTimeout = 15 * time.Second
	// shutdownTimeout bounds how long shutdown waits for in-flight jobs before
	// canceling them.
	shutdownTimeout = 30 * time.Second
)

// Runner executes a backup job and returns its recorded result. *backup.Runner
// satisfies it; the interface exists so shutdown and notification behavior can
// be tested without a live remote executor.
type Runner interface {
	Run(ctx context.Context, job backup.Job, out io.Writer) (*storage.BackupRun, error)
}

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
	cron         *cron.Cron
	runner       Runner
	store        *backup.Store
	dispatcher   *notify.Dispatcher
	out          io.Writer
	mu           sync.Mutex
	jobs         map[cron.EntryID]backup.Job
	daemonCtx    context.Context
	daemonCancel context.CancelFunc

	// Asynchronous notification delivery. Jobs only enqueue events; a fixed
	// pool of workers delivers them so a slow webhook cannot hold the cron
	// entry and suppress the next trigger (audit N5).
	notifyQueue    chan notify.Event
	notifyStop     chan struct{}
	notifyStart    sync.Once
	notifyStopOnce sync.Once
	notifyWG       sync.WaitGroup
	// notifyClosed records that the dispatcher has been shut down. Producers are
	// supposed to stop first, so this only ever fires on a late event: without
	// it such an event would be enqueued into a queue no worker reads any more
	// and vanish without a word.
	notifyClosed atomic.Bool
}

// lockedWriter serializes writes to the scheduler's output. Cron jobs and the
// notification workers all write concurrently; a plain io.Writer shared by them
// (a *bytes.Buffer in tests, os.Stdout in the daemon) races under -race.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// New creates a new Scheduler instance.
func New(store *backup.Store, runner Runner, dispatcher *notify.Dispatcher, out io.Writer) *Scheduler {
	if out == nil {
		out = os.Stdout
	}
	out = &lockedWriter{w: out}

	cronLogger := cron.PrintfLogger(log.New(out, "[scheduler] ", log.LstdFlags))
	c := cron.New(
		cron.WithChain(
			cron.SkipIfStillRunning(cronLogger),
			cron.Recover(cronLogger),
		),
	)

	ctx, cancel := context.WithCancel(context.Background())

	return &Scheduler{
		cron:         c,
		runner:       runner,
		store:        store,
		dispatcher:   dispatcher,
		out:          out,
		jobs:         make(map[cron.EntryID]backup.Job),
		daemonCtx:    ctx,
		daemonCancel: cancel,
		notifyQueue:  make(chan notify.Event, notifyQueueSize),
		notifyStop:   make(chan struct{}),
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

// RegisterJobs reads all jobs from the backup store and registers those with a
// non-empty, valid Schedule.
//
// A job whose cron expression is invalid is skipped rather than aborting the
// whole daemon: the per-job problems are returned as warnings for the caller to
// print, and only when every scheduled job is invalid does RegisterJobs return a
// non-nil error (audit B10).
func (s *Scheduler) RegisterJobs() ([]ScheduledJob, []error, error) {
	if s.store == nil {
		return nil, nil, fmt.Errorf("backup store is nil")
	}

	allJobs, err := s.store.List()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list backup jobs: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear previously registered entries if any
	for id := range s.jobs {
		s.cron.Remove(id)
	}
	s.jobs = make(map[cron.EntryID]backup.Job)

	var registered []ScheduledJob
	var warnings []error
	scheduledCount := 0

	for _, j := range allJobs {
		scheduleSpec := strings.TrimSpace(j.Schedule)
		if scheduleSpec == "" {
			continue
		}
		scheduledCount++

		if err := ValidateSchedule(scheduleSpec); err != nil {
			warnings = append(warnings, fmt.Errorf("job %q skipped: %w", j.Name, err))
			continue
		}

		jobCopy := j
		entryID, err := s.cron.AddFunc(scheduleSpec, func() {
			_ = s.executeJob(s.daemonCtx, jobCopy)
		})
		if err != nil {
			warnings = append(warnings, fmt.Errorf("job %q skipped: %w", j.Name, err))
			continue
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

	if scheduledCount > 0 && len(registered) == 0 {
		return nil, warnings, fmt.Errorf("all %d scheduled job(s) have invalid schedules: %w", scheduledCount, errors.Join(warnings...))
	}

	return registered, warnings, nil
}

func (s *Scheduler) executeJob(ctx context.Context, job backup.Job) error {
	startTime := time.Now()
	_, _ = fmt.Fprintf(s.out, "[scheduler] >>> Triggering scheduled backup job %q (%s) at %s\n",
		job.Name, job.Server, startTime.Format("2006-01-02 15:04:05"))

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
		} else if rec != nil && (rec.Status == "failed" || rec.Status == "partial") {
			runRecordErr = fmt.Errorf("job %q completed with status %q: %s", job.Name, rec.Status, errMsg)
		}
	} else {
		runRecordErr = fmt.Errorf("runner is nil")
		errMsg = "runner is not initialized"
	}

	// Queue the notification for asynchronous delivery. Notifications must not
	// run inside the cron task body: a wedged webhook would otherwise hold the
	// entry until SkipIfStillRunning suppresses the next trigger (audit N5).
	s.enqueueNotification(notify.Event{
		JobName:         job.Name,
		Status:          runRecordStatus,
		Server:          job.Server,
		Snapshot:        snapshotID,
		DurationSeconds: durationSec,
		Error:           errMsg,
		Timestamp:       time.Now(),
	})

	if runRecordErr != nil {
		_, _ = fmt.Fprintf(s.out, "[scheduler] <<< Scheduled job %q finished with error: %v\n", job.Name, runRecordErr)
		return runRecordErr
	}

	_, _ = fmt.Fprintf(s.out, "[scheduler] <<< Scheduled job %q finished successfully (status: %s, duration: %.2fs)\n",
		job.Name, runRecordStatus, durationSec)
	return nil
}

// startDispatcher lazily launches the fixed notification worker pool. It is a
// no-op when no dispatcher is configured, and safe to call concurrently.
func (s *Scheduler) startDispatcher() {
	if s.dispatcher == nil {
		return
	}
	s.notifyStart.Do(func() {
		for range notifyWorkers {
			s.notifyWG.Add(1)
			go s.notifyWorker()
		}
	})
}

// enqueueNotification hands an event to the worker pool without blocking. When
// the bounded queue is full the notification is dropped with a warning so a
// backlog can never stall job execution or grow without bound.
func (s *Scheduler) enqueueNotification(event notify.Event) {
	if s.dispatcher == nil {
		return
	}
	if s.notifyClosed.Load() {
		_, _ = fmt.Fprintf(s.out, "[scheduler] Warning: notification dispatcher already stopped; dropping notification for job %q\n",
			event.JobName)
		return
	}
	s.startDispatcher()

	select {
	case s.notifyQueue <- event:
	default:
		_, _ = fmt.Fprintf(s.out, "[scheduler] Warning: notification queue full (%d pending); dropping notification for job %q\n",
			cap(s.notifyQueue), event.JobName)
	}
}

func (s *Scheduler) notifyWorker() {
	defer s.notifyWG.Done()
	for {
		select {
		case event := <-s.notifyQueue:
			s.deliverNotification(event)
		case <-s.notifyStop:
			// Producers have stopped by the time the dispatcher is shut down,
			// so draining the queue cannot race with new events.
			for {
				select {
				case event := <-s.notifyQueue:
					s.deliverNotification(event)
				default:
					return
				}
			}
		}
	}
}

// deliverNotification runs a single dispatch with its own bounded context so a
// wedged webhook cannot outlive notifyDeliveryTimeout.
func (s *Scheduler) deliverNotification(event notify.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), notifyDeliveryTimeout)
	defer cancel()
	if errs := s.dispatcher.Dispatch(ctx, event); len(errs) > 0 {
		for _, err := range errs {
			_, _ = fmt.Fprintf(s.out, "[scheduler] Warning: notification delivery failed: %v\n", err)
		}
	}
}

// shutdownDispatcher stops the worker pool after draining queued events, waiting
// at most timeout. Safe to call multiple times.
func (s *Scheduler) shutdownDispatcher(timeout time.Duration) {
	if s.dispatcher == nil {
		return
	}
	// Marked before notifyStop closes, so an enqueue racing with shutdown is
	// rejected with a warning instead of landing in a queue nobody drains.
	s.notifyClosed.Store(true)
	s.notifyStopOnce.Do(func() { close(s.notifyStop) })

	done := make(chan struct{})
	go func() {
		s.notifyWG.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		_, _ = fmt.Fprintf(s.out, "[scheduler] Warning: notification dispatcher did not drain within %s (%d pending); exiting anyway\n",
			timeout, len(s.notifyQueue))
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

// Stop stops accepting new triggers and returns a context that is closed once
// all in-flight jobs have completed.
//
// The daemon context is canceled only after those jobs finish, so a graceful
// shutdown does not abort a backup mid-run (audit B1).
func (s *Scheduler) Stop() context.Context {
	stopCtx := s.cron.Stop()

	go func() {
		<-stopCtx.Done()
		if s.daemonCancel != nil {
			s.daemonCancel()
		}
	}()

	return stopCtx
}

// Run blocks until the provided context is canceled, handling graceful shutdown.
func (s *Scheduler) Run(ctx context.Context) error {
	registered, warnings, err := s.RegisterJobs()
	for _, warning := range warnings {
		_, _ = fmt.Fprintf(s.out, "[scheduler] ⚠️ %v\n", warning)
	}
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
	case <-time.After(shutdownTimeout):
		_, _ = fmt.Fprintf(s.out, "[scheduler] ⚠️ Graceful shutdown timed out after %s; canceling in-flight jobs.\n", shutdownTimeout)
		if s.daemonCancel != nil {
			s.daemonCancel()
		}
	}

	// In-flight jobs have stopped producing notifications, so drain the queue
	// before returning. The caller closes its database after Run returns, and
	// the notification workers must not outlive that.
	s.shutdownDispatcher(dispatcherDrainTimeout)

	return nil
}

// RunOnce executes all scheduled jobs once immediately without waiting for their cron triggers.
func (s *Scheduler) RunOnce(ctx context.Context) error {
	if s.store == nil {
		return fmt.Errorf("backup store is nil")
	}

	// RunOnce has no cron loop to stop, so it must drain queued notifications
	// itself before returning (the --once command exits right after).
	defer s.shutdownDispatcher(dispatcherDrainTimeout)

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

	total := len(scheduledJobs)
	if total == 0 {
		_, _ = fmt.Fprintln(s.out, "[scheduler] No scheduled backup jobs found.")
		return nil
	}

	_, _ = fmt.Fprintf(s.out, "[scheduler] Executing %d scheduled job(s) sequentially (--once mode)...\n", total)

	var firstErr error
	var successCount int
	var failureCount int

	for _, job := range scheduledJobs {
		select {
		case <-ctx.Done():
			_, _ = fmt.Fprintf(s.out, "[scheduler] ⚠️ Execution cancelled (%d/%d jobs processed: %d succeeded, %d failed): %v\n",
				successCount+failureCount, total, successCount, failureCount, ctx.Err())
			if firstErr != nil {
				return fmt.Errorf("run cancelled (%w), earlier error: %v", ctx.Err(), firstErr)
			}
			return ctx.Err()
		default:
		}

		if err := s.executeJob(ctx, job); err != nil {
			failureCount++
			if firstErr == nil {
				firstErr = err
			}
			if ctx.Err() != nil {
				_, _ = fmt.Fprintf(s.out, "[scheduler] ⚠️ Execution cancelled during job %q (%d/%d jobs processed: %d succeeded, %d failed): %v\n",
					job.Name, successCount+failureCount, total, successCount, failureCount, ctx.Err())
				if firstErr != nil && !errors.Is(firstErr, ctx.Err()) {
					return fmt.Errorf("run cancelled (%w), job %q error: %v", ctx.Err(), job.Name, firstErr)
				}
				return ctx.Err()
			}
		} else {
			successCount++
		}
	}

	_, _ = fmt.Fprintf(s.out, "[scheduler] Finished %d scheduled job(s) (%d succeeded, %d failed).\n",
		total, successCount, failureCount)

	return firstErr
}
