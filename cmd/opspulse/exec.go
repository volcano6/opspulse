package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
)

var (
	execTimeout        time.Duration
	execFilter         string
	execParallel       int
	execIncludeSkipped bool
)

var execCmd = &cobra.Command{
	Use:   "exec [server] <command...>",
	Short: "Execute a command on remote server(s)",
	Long: `Execute arbitrary shell commands on a specified remote server or across multiple
servers matched by a filter.

Examples:
  ops exec vps-1 uptime                          # Single server
  ops exec --filter all "uptime"                 # All servers in parallel
  ops exec --filter "provider=racknerd" "df -h"  # Filter by label or tag
  ops exec -f all -p 10 "docker ps -q | wc -l"   # Concurrency control`,
	Args: cobra.ArbitraryArgs,
	RunE: func(_ *cobra.Command, args []string) error {
		store := server.NewDefaultStore()

		if execFilter != "" {
			if len(args) < 1 {
				return errors.New("command is required when using --filter (e.g. ops exec --filter all 'uptime')")
			}
			commandStr := strings.Join(args, " ")
			return executeFiltered(store, execFilter, commandStr, execParallel, execTimeout, execIncludeSkipped)
		}

		if len(args) < 2 {
			return errors.New("accepts at least 2 arg(s), received " + strconv.Itoa(len(args)) +
				" (Usage: ops exec <server> <command...> or ops exec --filter <filter> <command...>)")
		}

		serverName := args[0]
		commandStr := strings.Join(args[1:], " ")

		srv, err := store.Get(serverName)
		if err != nil {
			return err
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		if execTimeout > 0 {
			var cancelTimeout context.CancelFunc
			ctx, cancelTimeout = context.WithTimeout(ctx, execTimeout)
			defer cancelTimeout()
		}

		exec := executor.NewSSHExecutor().WithServerResolver(store.Get)
		target := executor.NewServerTarget(*srv)

		res, err := exec.Execute(ctx, target, "exec", commandStr, os.Stdout)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("execution error on %s: %w", serverName, err)
		}
		if !res.Success {
			return fmt.Errorf("command failed on %s (exit code %d): %v", serverName, res.ExitCode, res.Error)
		}

		return nil
	},
}

var serverColors = []string{
	"\033[36m", // Cyan
	"\033[33m", // Yellow
	"\033[32m", // Green
	"\033[35m", // Magenta
	"\033[34m", // Blue
	"\033[96m", // Bright Cyan
	"\033[93m", // Bright Yellow
	"\033[92m", // Bright Green
}

// LinePrefixWriter writes each line with a synchronized, color-coded prefix.
type LinePrefixWriter struct {
	prefix string
	color  string
	out    io.Writer
	mu     *sync.Mutex
	buf    bytes.Buffer
}

// NewLinePrefixWriter creates a LinePrefixWriter writing to out protected by mu.
func NewLinePrefixWriter(prefix, color string, out io.Writer, mu *sync.Mutex) *LinePrefixWriter {
	return &LinePrefixWriter{
		prefix: prefix,
		color:  color,
		out:    out,
		mu:     mu,
	}
}

func (w *LinePrefixWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for _, b := range p {
		w.buf.WriteByte(b)
		if b == '\n' {
			line := w.buf.String()
			w.buf.Reset()
			w.writeFormattedLine(line)
		}
	}
	return len(p), nil
}

func (w *LinePrefixWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.buf.Len() > 0 {
		line := w.buf.String()
		w.buf.Reset()
		if !strings.HasSuffix(line, "\n") {
			line += "\n"
		}
		w.writeFormattedLine(line)
	}
}

func (w *LinePrefixWriter) writeFormattedLine(line string) {
	prefixStr := fmt.Sprintf("[%s] ", w.prefix)
	if w.color != "" {
		prefixStr = fmt.Sprintf("%s[%s]\033[0m ", w.color, w.prefix)
	}
	_, _ = fmt.Fprint(w.out, prefixStr+line)
}

// selectBatchServers filters a server list for batch operations.
// Servers with SkipBatch=true are excluded unless includeSkipped is true.
func selectBatchServers(servers []server.Server, filter string, includeSkipped bool) (targets []server.Server, skipped []string) {
	for _, s := range servers {
		if !s.MatchFilter(filter) {
			continue
		}
		if s.SkipBatch && !includeSkipped {
			skipped = append(skipped, s.Name)
			continue
		}
		targets = append(targets, s)
	}
	return targets, skipped
}

func executeFiltered(store *server.Store, filter, commandStr string, parallel int, timeout time.Duration, includeSkipped bool) error {
	allServers, err := store.List()
	if err != nil {
		return err
	}

	targets, skipped := selectBatchServers(allServers, filter, includeSkipped)

	if len(skipped) > 0 {
		fmt.Printf("ℹ️  Skipped %d server(s) configured with skip_batch: %s (use --include-skipped to run on all)\n",
			len(skipped), strings.Join(skipped, ", "))
	}

	if len(targets) == 0 {
		fmt.Printf("No servers matched filter %q.\n", filter)
		return nil
	}

	if parallel <= 0 {
		parallel = 5
	}
	if parallel > len(targets) {
		parallel = len(targets)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if timeout > 0 {
		var cancelTimeout context.CancelFunc
		ctx, cancelTimeout = context.WithTimeout(ctx, timeout)
		defer cancelTimeout()
	}

	startTime := time.Now()
	var (
		succeeded int32
		failed    int32
		sharedMu  sync.Mutex
		wg        sync.WaitGroup
	)

	sem := make(chan struct{}, parallel)

	for i, srv := range targets {
		wg.Add(1)
		color := serverColors[i%len(serverColors)]

		go func(s server.Server, clr string) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				atomic.AddInt32(&failed, 1)
				return
			}

			writer := NewLinePrefixWriter(s.Name, clr, os.Stdout, &sharedMu)
			defer writer.Flush()

			exec := executor.NewSSHExecutor().WithServerResolver(store.Get)
			target := executor.NewServerTarget(s)

			res, runErr := exec.Execute(ctx, target, "exec-batch", commandStr, writer)
			if runErr != nil || (res != nil && !res.Success) {
				atomic.AddInt32(&failed, 1)
				sharedMu.Lock()
				errMsg := ""
				if runErr != nil {
					errMsg = runErr.Error()
				} else if res != nil && res.Error != nil {
					errMsg = res.Error.Error()
				}
				if clr != "" {
					fmt.Printf("%s[%s]\033[0m ❌ Command failed: %s\n", clr, s.Name, errMsg)
				} else {
					fmt.Printf("[%s] ❌ Command failed: %s\n", s.Name, errMsg)
				}
				sharedMu.Unlock()
			} else {
				atomic.AddInt32(&succeeded, 1)
			}
		}(srv, color)
	}

	wg.Wait()
	elapsed := time.Since(startTime)

	fmt.Printf("\n--> Summary: %d succeeded, %d failed across %d servers (took %.2fs)\n",
		succeeded, failed, len(targets), elapsed.Seconds())

	if failed > 0 {
		return fmt.Errorf("%d server(s) failed during execution", failed)
	}

	return nil
}

func completeExecArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if execFilter != "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if len(args) == 0 {
		return completeServerNames(cmd, args, toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func init() {
	execCmd.Flags().DurationVarP(&execTimeout, "timeout", "T", 60*time.Second, "Command execution timeout (0 to disable)")
	execCmd.Flags().StringVarP(&execFilter, "filter", "f", "", "Filter target servers (e.g. 'all', 'provider=racknerd', or tag)")
	execCmd.Flags().IntVarP(&execParallel, "parallel", "p", 5, "Maximum number of parallel server executions")
	execCmd.Flags().BoolVar(&execIncludeSkipped, "include-skipped", false, "Include servers configured with skip_batch in batch execution")
	execCmd.ValidArgsFunction = completeExecArgs
	rootCmd.AddCommand(execCmd)
}
