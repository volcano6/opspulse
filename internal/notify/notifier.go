package notify

import (
	"context"
	"time"
)

// Event kinds distinguish the operation an event came from. The zero value is
// a backup event, so every existing producer keeps its current payload name.
const (
	EventKindBackup  = "backup"
	EventKindRestore = "restore"
)

// StepStatus is one step of a multi-step operation (currently a restore) as it
// travels to a notification channel: the same vocabulary the CLI prints, so an
// alert can be read without opening the local database.
type StepStatus struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
	NextStep string `json:"next_step,omitempty"`
}

// Event captures the context and execution results of a job to be notified.
type Event struct {
	JobName         string    `json:"job_name"`
	Status          string    `json:"status"` // "success", "failed"
	Server          string    `json:"server"`
	Snapshot        string    `json:"snapshot,omitempty"`
	DurationSeconds float64   `json:"duration_seconds"`
	Error           string    `json:"error,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
	// Untracked marks a run whose audit record could not be created. Receivers
	// must not read such a "success" as a normal, audited success.
	Untracked bool `json:"untracked,omitempty"`
	// Kind is "backup" (default) or "restore".
	Kind string `json:"kind,omitempty"`
	// Steps carries the per-step outcome of a restore.
	Steps []StepStatus `json:"steps,omitempty"`
	// Warnings are non-fatal observations the operator should read.
	Warnings []string `json:"warnings,omitempty"`
	// Pending lists the work that is still outstanding.
	Pending []string `json:"pending,omitempty"`
}

// Notifier defines the interface for delivering notifications to a specific channel.
type Notifier interface {
	Send(ctx context.Context, event Event) error
	Channel() Channel
}
