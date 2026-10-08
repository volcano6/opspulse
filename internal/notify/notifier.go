package notify

import (
	"context"
	"time"
)

// Event captures the context and execution results of a backup job to be notified.
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
}

// Notifier defines the interface for delivering notifications to a specific channel.
type Notifier interface {
	Send(ctx context.Context, event Event) error
	Channel() Channel
}
