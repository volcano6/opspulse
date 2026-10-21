package backup

import (
	"fmt"
	"io"
	"strings"

	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/storage"
)

// Restore step kinds. A restore is reported as four independent steps so a
// partial recovery can be diagnosed from the result itself instead of the log
// file: the file restore, the named volumes, the database import and the
// Compose start.
const (
	RestoreStepFiles    = "files"
	RestoreStepVolumes  = "volumes"
	RestoreStepDatabase = "database"
	RestoreStepCompose  = "compose"
)

// Restore step statuses.
//
//   - success: the step ran and finished.
//   - failed: the step ran and failed.
//   - skipped: the step was deliberately not attempted (nothing to do, or the
//     operator asked for files only). It never degrades the aggregate status.
//   - blocked: the step could not run because a prerequisite was missing (an
//     earlier step failed, no Compose engine on the target).
//   - manual: the step cannot be automated and is left to the operator.
const (
	RestoreStepSuccess = "success"
	RestoreStepFailed  = "failed"
	RestoreStepSkipped = "skipped"
	RestoreStepBlocked = "blocked"
	RestoreStepManual  = "manual"
)

// restoreStepOrder fixes the presentation order of the steps. It is the order
// of the restore result and the notification payload, not necessarily the order
// the steps execute in (the Compose start has to precede the database import).
var restoreStepOrder = []struct{ kind, label string }{
	{RestoreStepFiles, "文件恢复"},
	{RestoreStepVolumes, "volume 恢复"},
	{RestoreStepDatabase, "数据库导入"},
	{RestoreStepCompose, "Compose 启动"},
}

// RestoreStep is the outcome of one stage of a restore.
type RestoreStep struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
	NextStep string `json:"next_step,omitempty"`
}

// RestoreResult is the full outcome of one restore attempt: the audit record
// persisted in SQLite (embedded, so the historical status/stage contract is
// unchanged) plus the per-step report the console output and the notification
// payload are built from.
type RestoreResult struct {
	*storage.RestoreRun
	Steps    []RestoreStep `json:"steps"`
	Warnings []string      `json:"warnings,omitempty"`
	Pending  []string      `json:"pending,omitempty"`
	// Untracked marks a completed restore whose final audit record could not be
	// persisted. The embedded run may be absent from SQLite, so the notification
	// path must carry this flag explicitly.
	Untracked bool `json:"untracked,omitempty"`
}

// newRestoreReport seeds every step as skipped, so a step the run never reached
// is still reported instead of silently disappearing from the result.
func newRestoreReport(run *storage.RestoreRun) *RestoreResult {
	result := &RestoreResult{RestoreRun: run}
	for _, step := range restoreStepOrder {
		result.Steps = append(result.Steps, RestoreStep{
			Kind:   step.kind,
			Label:  step.label,
			Status: RestoreStepSkipped,
			Detail: "未执行",
		})
	}
	return result
}

// setStep overwrites one step's outcome.
func (res *RestoreResult) setStep(kind, status, detail, nextStep string) {
	for i := range res.Steps {
		if res.Steps[i].Kind == kind {
			res.Steps[i].Status = status
			res.Steps[i].Detail = detail
			res.Steps[i].NextStep = nextStep
			return
		}
	}
}

// stepStatus returns the current status of one step ("" when unknown).
func (res *RestoreResult) stepStatus(kind string) string {
	for _, step := range res.Steps {
		if step.Kind == kind {
			return step.Status
		}
	}
	return ""
}

// addWarning records a non-fatal observation. Warnings are part of the result
// and the notification, not just console noise.
func (res *RestoreResult) addWarning(msg string) {
	if strings.TrimSpace(msg) == "" {
		return
	}
	res.Warnings = append(res.Warnings, msg)
}

// addPending records work the operator still has to finish.
func (res *RestoreResult) addPending(msg string) {
	if strings.TrimSpace(msg) == "" {
		return
	}
	res.Pending = append(res.Pending, msg)
}

// warnf records a warning and mirrors it to the console, so the notification
// and the terminal tell the same story.
func (res *RestoreResult) warnf(out io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	res.addWarning(strings.TrimSpace(msg))
	if out != nil {
		_, _ = fmt.Fprintln(out, msg)
	}
}

// skipContainerSteps marks the three post-file steps as deliberately skipped,
// with the scenario-specific reason they did not run.
func (res *RestoreResult) skipContainerSteps(reason string) {
	for _, kind := range []string{RestoreStepVolumes, RestoreStepDatabase, RestoreStepCompose} {
		res.setStep(kind, RestoreStepSkipped, reason, "")
	}
}

// blockSteps marks steps that could not run because an earlier step failed or
// because they depend on a missing prerequisite.
func (res *RestoreResult) blockSteps(reason string, kinds ...string) {
	for _, kind := range kinds {
		res.setStep(kind, RestoreStepBlocked, reason, "")
	}
}

// aggregateStatus derives the backward-compatible single status from the steps.
// It keeps the historical vocabulary (success / partial / failed / dry-run)
// for completed steps; execution cancellation is mapped to "canceled" by the
// runner before this helper is called. A failed file restore is a failure,
// anything else that failed or was blocked is a partial recovery, and a
// deliberately skipped step is not a degradation.
func (res *RestoreResult) aggregateStatus(dryRun bool) string {
	if dryRun {
		return "dry-run"
	}
	if res.stepStatus(RestoreStepFiles) == RestoreStepFailed {
		return "failed"
	}
	for _, step := range res.Steps {
		if step.Status == RestoreStepFailed || step.Status == RestoreStepBlocked {
			return "partial"
		}
	}
	return "success"
}

// classifyStepStatus maps a failed remote step onto "blocked" when the target
// is missing a prerequisite (the generated scripts exit 127 for a missing
// Compose engine or helper image) and onto "failed" otherwise.
func classifyStepStatus(res *executor.Result) string {
	if res != nil && res.ExitCode == 127 {
		return RestoreStepBlocked
	}
	return RestoreStepFailed
}

// writeRestoreResult renders the per-step outcome. It is the human-readable
// counterpart of the notification payload: every step with its status, the
// operator's next command for the steps that need one, then warnings and the
// work that is still outstanding.
func writeRestoreResult(w io.Writer, res *RestoreResult, durationSeconds float64) {
	_, _ = fmt.Fprintf(w, "\n恢复结果（任务 %q，快照 %s，目标 %s，耗时 %.2fs，总状态：%s）\n",
		res.JobName, shortSnapshotID(res.SnapshotID), res.TargetServer, durationSeconds, strings.ToUpper(res.Status))

	for i, step := range res.Steps {
		_, _ = fmt.Fprintf(w, "  %d. [%s] %s：%s\n", i+1, step.Status, step.Label, step.Detail)
		if step.NextStep != "" {
			_, _ = fmt.Fprintf(w, "     下一步：%s\n", step.NextStep)
		}
	}

	if len(res.Warnings) > 0 {
		_, _ = fmt.Fprintln(w, "  警告：")
		for _, warning := range res.Warnings {
			_, _ = fmt.Fprintf(w, "    - %s\n", warning)
		}
	}

	if len(res.Pending) > 0 {
		_, _ = fmt.Fprintln(w, "  未完成事项：")
		for _, item := range res.Pending {
			_, _ = fmt.Fprintf(w, "    - %s\n", item)
		}
	}
}

// restoreStartHint renders the command that starts the services this run left
// stopped, carrying the operator's own target options over.
func restoreStartHint(jobName string, opts RestoreOptions) string {
	parts := []string{"ops", "restore", "run", jobName}
	if opts.TargetServer != "" {
		parts = append(parts, "--target-server", opts.TargetServer)
	}
	if opts.TargetPath != "" {
		parts = append(parts, "--target-path", opts.TargetPath)
	}
	if opts.AliasName != "" {
		parts = append(parts, "--as", opts.AliasName)
	}
	return strings.Join(append(parts, "--start"), " ")
}

// shortSnapshotID truncates a snapshot ID for display.
func shortSnapshotID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
