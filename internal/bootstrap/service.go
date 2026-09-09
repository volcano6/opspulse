package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/shellquote"
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
	localExecutor  *executor.LocalExecutor
}

// NewService creates a new bootstrap Service.
func NewService(serverStore *server.Store, templateLoader *template.Loader, exec executor.Executor) *Service {
	return &Service{
		serverStore:    serverStore,
		templateLoader: templateLoader,
		executor:       exec,
		localExecutor:  executor.NewLocalExecutor(),
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

// ResolveTarget determines whether the target is local or a remote server from inventory.
func (s *Service) ResolveTarget(serverName string) (executor.Target, error) {
	if serverName == "local" || serverName == "" {
		return executor.NewLocalTarget(), nil
	}

	if s.serverStore == nil {
		return executor.Target{}, fmt.Errorf("serverStore is nil, cannot resolve %q", serverName)
	}

	srv, err := s.serverStore.Get(serverName)
	if err != nil {
		return executor.Target{}, fmt.Errorf("server %q not found in inventory: %w", serverName, err)
	}

	return executor.NewServerTarget(*srv), nil
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
	var targetServers []executor.Target
	for _, name := range opts.ServerNames {
		target, err := s.ResolveTarget(name)
		if err != nil {
			return nil, err
		}
		targetServers = append(targetServers, target)
	}

	// 2. Resolve all templates and inline arguments in specified order
	type resolvedTemplate struct {
		template.Template
		DisplayName string
		Argument    string
		ExecContent string
	}

	var targetTemplates []resolvedTemplate
	for _, spec := range opts.TemplateNames {
		name := spec
		var arg string
		if idx := strings.Index(spec, ":"); idx != -1 {
			name = spec[:idx]
			arg = spec[idx+1:]
		} else if idx := strings.Index(spec, "="); idx != -1 {
			name = spec[:idx]
			arg = spec[idx+1:]
		}

		tmpl, err := s.templateLoader.Get(name)
		if err != nil {
			return nil, fmt.Errorf("template %q not found: %w", name, err)
		}

		execContent := tmpl.Content
		if arg != "" {
			quotedArg := shellquote.Quote(arg)
			execContent = fmt.Sprintf("set -- %s\nexport SCRIPT_ARG=%s\n%s", quotedArg, quotedArg, tmpl.Content)
		}

		targetTemplates = append(targetTemplates, resolvedTemplate{
			Template:    *tmpl,
			DisplayName: spec,
			Argument:    arg,
			ExecContent: execContent,
		})
	}

	totalStartTime := time.Now()
	summary := &Summary{
		IsDryRun: opts.DryRun,
	}

	if consoleOut == nil {
		consoleOut = io.Discard
	}

	// 3. Sequential server execution loop
	for serverIdx, target := range targetServers {
		srvName := target.Name
		srvAddr := target.Name
		if target.Server != nil {
			srvAddr = target.Server.Address()
		} else {
			srvAddr = "localhost"
		}

		_, _ = fmt.Fprintf(consoleOut, "\n[%d/%d] >>> Starting bootstrap on server: %s (%s) <<<\n",
			serverIdx+1, len(targetServers), srvName, srvAddr)

		var logFile *os.File
		var logFilePath string

		if !opts.DryRun {
			var err error
			logFilePath, err = executor.LogPathFor(srvName, time.Now())
			if err == nil {
				logFile, _ = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
				if logFile != nil {
					_, _ = fmt.Fprintf(logFile, "=== Bootstrap Log for Server %s (%s) at %s ===\n\n",
						srvName, srvAddr, time.Now().Format(time.RFC3339))
				}
			}
		}

		serverFailed := false

		// 4. Sequential template execution loop on current server
		for tmplIdx, targetTmpl := range targetTemplates {
			if serverFailed && opts.StopOnError {
				// Record skipped template
				summary.Results = append(summary.Results, executor.Result{
					ServerName: srvName,
					Template:   targetTmpl.DisplayName,
					Success:    false,
					Error:      errors.New("skipped due to previous error"),
					LogPath:    logFilePath,
				})
				summary.FailureCount++
				continue
			}

			_, _ = fmt.Fprintf(consoleOut, "\n--> [%s] Running template [%d/%d]: %s (v%d) - %s\n",
				srvName, tmplIdx+1, len(targetTemplates), targetTmpl.DisplayName, targetTmpl.Metadata.Version, targetTmpl.Metadata.Description)

			if opts.DryRun {
				_, _ = fmt.Fprintf(consoleOut, "[DRY-RUN] Would execute script (%d bytes) on %s\n",
					len(targetTmpl.ExecContent), srvAddr)
				summary.Results = append(summary.Results, executor.Result{
					ServerName: srvName,
					Template:   targetTmpl.DisplayName,
					Success:    true,
					Duration:   10 * time.Millisecond,
					LogPath:    "dry-run",
				})
				summary.SuccessCount++
				continue
			}

			// Setup prefixed console output & log file writer
			prefix := fmt.Sprintf("[%s] ", srvName)
			prefixedConsole := executor.NewPrefixedWriter(prefix, consoleOut)

			var multiWriter io.Writer = prefixedConsole
			if logFile != nil {
				_, _ = fmt.Fprintf(logFile, "\n--- Template: %s ---\n", targetTmpl.DisplayName)
				multiWriter = io.MultiWriter(prefixedConsole, logFile)
			}

			// Inject server execution environment variables (port, name, host, user)
			serverPort := 22
			serverHost := "localhost"
			serverUser := "root"
			if target.Server != nil {
				if target.Server.Port > 0 {
					serverPort = target.Server.Port
				}
				serverHost = target.Server.Host
				serverUser = target.Server.User
			}

			serverEnv := fmt.Sprintf("export OPS_SERVER_NAME=%s\nexport OPS_SERVER_HOST=%s\nexport OPS_SSH_PORT=%d\nexport OPS_SERVER_USER=%s\n",
				shellquote.Quote(srvName),
				shellquote.Quote(serverHost),
				serverPort,
				shellquote.Quote(serverUser),
			)
			execContent := serverEnv + targetTmpl.ExecContent

			// Inject privilege guard and handle local sudo
			execToUse := s.executor
			if target.IsLocal {
				execToUse = s.localExecutor
				
				// Perform a quick pre-authentication check so the user isn't prompted repeatedly during execution
				sudoCheck := exec.CommandContext(ctx, "sudo", "-v")
				sudoCheck.Stdout = consoleOut
				sudoCheck.Stderr = consoleOut
				_ = sudoCheck.Run() // Ignore error here, it will fail in the script if not authenticated

				privGuard := "if [ \"$(id -u)\" -ne 0 ]; then\n  echo \"Elevating privileges for local bootstrap...\" >&2\n  exec sudo -E bash -s << 'EOF_OPSPULSE_BOOTSTRAP'\n"
				execContent = privGuard + execContent + "\nEOF_OPSPULSE_BOOTSTRAP\nfi\n"
			} else {
				privGuard := fmt.Sprintf("if [ \"$(id -u)\" -ne 0 ]; then\n  echo \"Error: bootstrap template %s requires root privileges.\" >&2\n  echo \"Current user is not root and lacks passwordless sudo (NOPASSWD). Please switch server user to root or configure sudoers.\" >&2\n  exit 1\nfi\n", shellquote.Quote(targetTmpl.DisplayName))
				execContent = privGuard + execContent
			}

			// Execute template via Executor interface
			res, err := execToUse.Execute(ctx, target, targetTmpl.DisplayName, execContent, multiWriter)
			_ = prefixedConsole.Flush()

			res.LogPath = logFilePath
			summary.Results = append(summary.Results, *res)

			if err != nil || !res.Success {
				serverFailed = true
				summary.FailureCount++
				_, _ = fmt.Fprintf(consoleOut, "[%s] ❌ Template %s failed: %v (Duration: %.2fs)\n",
					srvName, targetTmpl.DisplayName, res.Error, res.Duration.Seconds())
			} else {
				summary.SuccessCount++
				_, _ = fmt.Fprintf(consoleOut, "[%s] ✅ Template %s completed successfully (Duration: %.2fs)\n",
					srvName, targetTmpl.DisplayName, res.Duration.Seconds())
			}
		}

		if logFile != nil {
			_, _ = fmt.Fprintf(logFile, "\n=== Finished Bootstrap for Server %s at %s ===\n",
				srvName, time.Now().Format(time.RFC3339))
			_ = logFile.Close()
		}

		if serverFailed && opts.StopOnError {
			// Record skipped remaining servers and templates
			for remIdx := serverIdx + 1; remIdx < len(targetServers); remIdx++ {
				remTarget := targetServers[remIdx]
				for _, targetTmpl := range targetTemplates {
					summary.Results = append(summary.Results, executor.Result{
						ServerName: remTarget.Name,
						Template:   targetTmpl.DisplayName,
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
