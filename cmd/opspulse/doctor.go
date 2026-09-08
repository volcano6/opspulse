package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/doctor"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
)

var (
	doctorFilter   string
	doctorParallel int
	doctorTimeout  time.Duration
)

var doctorCmd = &cobra.Command{
	Use:   "doctor [flags]",
	Short: "Inspect cluster health (SSH latency, disk usage, Docker status)",
	Long: `Runs a non-intrusive health inspection across configured servers.
Checks SSH connectivity, root filesystem disk utilization, and Docker daemon status.

Examples:
  ops doctor                       # Inspect all servers
  ops doctor -f "provider=oracle"  # Inspect specific cluster
  ops doctor -p 10                 # Control concurrency`,
	RunE: func(_ *cobra.Command, _ []string) error {
		store := server.NewDefaultStore()
		servers, err := store.List()
		if err != nil {
			return err
		}

		var targets []server.Server
		for _, s := range servers {
			if s.MatchFilter(doctorFilter) {
				targets = append(targets, s)
			}
		}

		if len(targets) == 0 {
			if doctorFilter != "" && doctorFilter != "all" {
				fmt.Printf("No servers matched filter %q.\n", doctorFilter)
			} else {
				fmt.Println("No servers configured. Add one using 'ops add <name> <host>'")
			}
			return nil
		}

		if doctorParallel <= 0 {
			doctorParallel = 5
		}
		if doctorParallel > len(targets) {
			doctorParallel = len(targets)
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		fmt.Printf("🩺 Running health checks on %d server(s) (concurrency: %d)...\n\n", len(targets), doctorParallel)
		startTime := time.Now()

		results := make([]doctor.ServerHealth, len(targets))
		sem := make(chan struct{}, doctorParallel)
		var wg sync.WaitGroup

		exec := executor.NewSSHExecutor().WithServerResolver(store.Get)

		for i, srv := range targets {
			wg.Add(1)
			go func(idx int, s server.Server) {
				defer wg.Done()

				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					results[idx] = doctor.ServerHealth{
						Name:          s.Name,
						Host:          s.Host,
						Port:          s.Port,
						ReachErr:      ctx.Err(),
						OverallStatus: doctor.StatusUnreachable,
					}
					return
				}

				probeCtx := ctx
				if doctorTimeout > 0 {
					var cancelTimeout context.CancelFunc
					probeCtx, cancelTimeout = context.WithTimeout(ctx, doctorTimeout)
					defer cancelTimeout()
				}

				results[idx] = doctor.InspectServer(probeCtx, s, exec)
			}(i, srv)
		}

		wg.Wait()
		elapsed := time.Since(startTime)

		if err := renderDoctorTable(os.Stdout, results); err != nil {
			return err
		}

		var healthyCount, warnCount, critCount, unreachCount int
		for _, r := range results {
			switch r.OverallStatus {
			case doctor.StatusHealthy:
				healthyCount++
			case doctor.StatusWarning:
				warnCount++
			case doctor.StatusCritical:
				critCount++
			case doctor.StatusUnreachable:
				unreachCount++
			}
		}

		fmt.Printf("\n--> Checked %d servers in %.2fs: %d Healthy, %d Warning, %d Critical, %d Unreachable\n",
			len(results), elapsed.Seconds(), healthyCount, warnCount, critCount, unreachCount)

		if critCount > 0 || unreachCount > 0 {
			return fmt.Errorf("cluster health check detected %d critical / unreachable server(s)", critCount+unreachCount)
		}

		return nil
	},
}

func renderDoctorTable(w io.Writer, results []doctor.ServerHealth) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SERVER\tADDRESS\tPING\tDISK (ROOT)\tDOCKER\tSTATUS")
	_, _ = fmt.Fprintln(tw, "------\t-------\t----\t-----------\t------\t------")

	for _, r := range results {
		addr := fmt.Sprintf("%s:%d", r.Host, r.Port)
		if r.Port == 0 || r.Port == 22 {
			addr = r.Host
		}

		ping := r.FormatLatency()
		disk := r.FormatDisk()
		dockerStr := r.FormatDocker()
		status := r.FormatStatus()

		if r.OverallStatus == doctor.StatusUnreachable {
			ping = "-"
			disk = "-"
			dockerStr = "-"
		}

		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Name, addr, ping, disk, dockerStr, status)
	}

	return tw.Flush()
}

func init() {
	doctorCmd.Flags().StringVarP(&doctorFilter, "filter", "f", "all", "Filter target servers (e.g. 'all', 'provider=oracle', tag)")
	doctorCmd.Flags().IntVarP(&doctorParallel, "parallel", "p", 5, "Maximum number of concurrent server probes")
	doctorCmd.Flags().DurationVarP(&doctorTimeout, "timeout", "T", 15*time.Second, "Per-server probe timeout")

	rootCmd.AddCommand(doctorCmd)
}
