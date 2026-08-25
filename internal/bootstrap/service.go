package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/template"
)

var (
	// ErrNoServersSpecified is returned when no servers are specified for bootstrap.
	ErrNoServersSpecified = errors.New("no servers specified")
	// ErrNoTemplatesSpecified is returned when no templates are specified for bootstrap.
	ErrNoTemplatesSpecified = errors.New("no templates specified")
)

// Service coordinates the execution of script templates across servers.
type Service struct {
	serverStore    *server.Store
	templateLoader *template.Loader
	executor       executor.Executor
}

// NewService creates a new bootstrap Service.
func NewService(serverStore *server.Store, templateLoader *template.Loader, exec executor.Executor) *Service {
	return &Service{
		serverStore:    serverStore,
		templateLoader: templateLoader,
		executor:       exec,
	}
}

// NewDefaultService initializes a bootstrap Service using default stores and loaders.
func NewDefaultService() *Service {
	return NewService(
		server.NewDefaultStore(),
		template.NewDefaultLoader(),
		executor.NewSSHExecutor(),
	)
}

// Run executes the bootstrap workflow according to the provided options.
func (s *Service) Run(ctx context.Context, opts RunOptions, consoleOut io.Writer) (*Summary, error) {
	if len(opts.ServerNames) == 0 {
		return nil, ErrNoServersSpecified
	}
	if len(opts.TemplateNames) == 0 {
		return nil, ErrNoTemplatesSpecified
	}

	// 1. Resolve all target servers
	var targetServers []server.Server
	for _, name := range opts.ServerNames {
		srv, err := s.serverStore.Get(name)
		if err != nil {
			return nil, fmt.Errorf("server %q not found in inventory: %w", name, err)
		}
		targetServers = append(targetServers, *srv)
	}

	// 2. Resolve all templates in specified order
	var targetTemplates []template.Template
	for _, name := range opts.TemplateNames {
		tmpl, err := s.templateLoader.Get(name)
		if err != nil {
			return nil, fmt.Errorf("template %q not found: %w", name, err)
		}
		targetTemplates = append(targetTemplates, *tmpl)
	}

	totalStartTime := time.Now()
	summary := &Summary{
		IsDryRun: opts.DryRun,
	}

	if consoleOut == nil {
		consoleOut = io.Discard
	}

	// 3. Sequential server execution loop
	for serverIdx, srv := range targetServers {
		_, _ = fmt.Fprintf(consoleOut, "\n[%d/%d] >>> Starting bootstrap on server: %s (%s) <<<\n",
			serverIdx+1, len(targetServers), srv.Name, srv.Address())

		var logFile *os.File
		var logFilePath string

		if !opts.DryRun {
			var err error
			logFilePath, err = executor.LogPathFor(srv.Name, time.Now())
			if err == nil {
				logFile, _ = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
				if logFile != nil {
					_, _ = fmt.Fprintf(logFile, "=== Bootstrap Log for Server %s (%s) at %s ===\n\n",
						srv.Name, srv.Address(), time.Now().Format(time.RFC3339))
				}
			}
		}

		serverFailed := false

		// 4. Sequential template execution loop on current server
		for tmplIdx, tmpl := range targetTemplates {
			if serverFailed && opts.StopOnError {
				// Record skipped template
				summary.Results = append(summary.Results, executor.Result{
					ServerName: srv.Name,
					Template:   tmpl.Metadata.Name,
					Success:    false,
					Error:      errors.New("skipped due to previous error"),
					LogPath:    logFilePath,
				})
				summary.FailureCount++
				continue
			}

			_, _ = fmt.Fprintf(consoleOut, "\n--> [%s] Running template [%d/%d]: %s (v%d) - %s\n",
				srv.Name, tmplIdx+1, len(targetTemplates), tmpl.Metadata.Name, tmpl.Metadata.Version, tmpl.Metadata.Description)

			if opts.DryRun {
				_, _ = fmt.Fprintf(consoleOut, "[DRY-RUN] Would execute script (%d bytes) on %s\n",
					len(tmpl.Content), srv.Address())
				summary.Results = append(summary.Results, executor.Result{
					ServerName: srv.Name,
					Template:   tmpl.Metadata.Name,
					Success:    true,
					Duration:   10 * time.Millisecond,
					LogPath:    "dry-run",
				})
				summary.SuccessCount++
				continue
			}

			// Setup prefixed console output & log file writer
			prefix := fmt.Sprintf("[%s] ", srv.Name)
			prefixedConsole := executor.NewPrefixedWriter(prefix, consoleOut)

			var multiWriter io.Writer = prefixedConsole
			if logFile != nil {
				_, _ = fmt.Fprintf(logFile, "\n--- Template: %s ---\n", tmpl.Metadata.Name)
				multiWriter = io.MultiWriter(prefixedConsole, logFile)
			}

			// Execute template via Executor interface
			target := executor.NewServerTarget(srv)
			res, err := s.executor.Execute(ctx, target, tmpl.Metadata.Name, tmpl.Content, multiWriter)
			_ = prefixedConsole.Flush()

			res.LogPath = logFilePath
			summary.Results = append(summary.Results, *res)

			if err != nil || !res.Success {
				serverFailed = true
				summary.FailureCount++
				_, _ = fmt.Fprintf(consoleOut, "[%s] ❌ Template %s failed: %v (Duration: %.2fs)\n",
					srv.Name, tmpl.Metadata.Name, res.Error, res.Duration.Seconds())
			} else {
				summary.SuccessCount++
				_, _ = fmt.Fprintf(consoleOut, "[%s] ✅ Template %s completed successfully (Duration: %.2fs)\n",
					srv.Name, tmpl.Metadata.Name, res.Duration.Seconds())
			}
		}

		if logFile != nil {
			_, _ = fmt.Fprintf(logFile, "\n=== Finished Bootstrap for Server %s at %s ===\n",
				srv.Name, time.Now().Format(time.RFC3339))
			_ = logFile.Close()
		}

		if serverFailed && opts.StopOnError {
			// Record skipped remaining servers and templates
			for remIdx := serverIdx + 1; remIdx < len(targetServers); remIdx++ {
				remSrv := targetServers[remIdx]
				for _, tmpl := range targetTemplates {
					summary.Results = append(summary.Results, executor.Result{
						ServerName: remSrv.Name,
						Template:   tmpl.Metadata.Name,
						Success:    false,
						Error:      errors.New("skipped due to previous server failure"),
					})
					summary.FailureCount++
				}
			}
			break
		}
	}

	summary.TotalDuration = time.Since(totalStartTime)
	return summary, nil
}
