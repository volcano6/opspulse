package bootstrap

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/volcano6/opspulse/internal/executor"
)

// Summary contains the aggregated outcome of a bootstrap run across all servers and templates.
type Summary struct {
	Results       []executor.Result
	TotalDuration time.Duration
	SuccessCount  int
	FailureCount  int
	IsDryRun      bool
}

// PrintTable writes a formatted summary table to the given writer.
func (s *Summary) PrintTable(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "\n==================== Bootstrap Summary ====================")
	_, _ = fmt.Fprintln(tw, "SERVER\tTEMPLATE\tSTATUS\tDURATION\tLOG FILE")
	_, _ = fmt.Fprintln(tw, "------\t--------\t------\t--------\t--------")

	for _, r := range s.Results {
		status := "SUCCESS"
		if s.IsDryRun {
			status = "DRY-RUN"
		} else if !r.Success {
			status = "FAILED"
		}

		durationStr := fmt.Sprintf("%.2fs", r.Duration.Seconds())
		logPath := r.LogPath
		if logPath == "" {
			logPath = "-"
		}

		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			r.ServerName,
			r.Template,
			status,
			durationStr,
			logPath,
		)
	}
	_ = tw.Flush()

	_, _ = fmt.Fprintln(w, "-----------------------------------------------------------")
	statusSummary := fmt.Sprintf("Total: %d | Succeeded: %d | Failed: %d | Total Duration: %.2fs",
		len(s.Results), s.SuccessCount, s.FailureCount, s.TotalDuration.Seconds())
	if s.IsDryRun {
		statusSummary += " (Dry Run)"
	}
	_, _ = fmt.Fprintln(w, statusSummary)
	_, _ = fmt.Fprintln(w, "===========================================================")
}
