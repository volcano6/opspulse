package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/docker"
	"github.com/volcano6/opspulse/internal/storage"
)

// ContainerBackupOptions defines parameters for backing up a Docker container.
type ContainerBackupOptions struct {
	Server        string // Server name from inventory or "local"
	ContainerName string // Name or ID of the container
	AliasName     string // Optional alias to rename the container in generated Compose and backup job
}

// ContainerBackupResult contains the results of the container backup operation.
type ContainerBackupResult struct {
	RunRecord    *storage.BackupRun
	JobName      string
	ServerName   string
	SnapshotID   string
	ComposePath  string
	IsCompose    bool
	IsDatabase   bool
	DatabaseDump string
	Paths        []string
}

// RunContainerBackup inspects a running container on the target, generates Compose specification
// (or resolves existing Compose project), dumps database if applicable, and executes a unified restic backup.
func (r *Runner) RunContainerBackup(
	ctx context.Context,
	opts ContainerBackupOptions,
	consoleOut io.Writer,
) (*ContainerBackupResult, error) {
	if consoleOut == nil {
		consoleOut = io.Discard
	}

	serverName := strings.TrimSpace(opts.Server)
	if serverName == "" {
		return nil, fmt.Errorf("server name cannot be empty")
	}
	containerName := strings.TrimSpace(opts.ContainerName)
	if containerName == "" {
		return nil, fmt.Errorf("container name cannot be empty")
	}

	finalName := strings.TrimSpace(opts.AliasName)
	if finalName == "" {
		finalName = containerName
	}

	target, err := r.ResolveTarget(serverName)
	if err != nil {
		return nil, err
	}

	execToUse := r.executor
	if target.IsLocal {
		execToUse = r.localExecutor
	}

	_, _ = fmt.Fprintf(consoleOut, "==> Inspecting container %q on %s...\n", containerName, serverName)

	// 1. Inspect container via docker inspect
	var inspectBuf bytes.Buffer
	inspectScript := fmt.Sprintf("docker inspect %q", containerName)
	inspectRes, inspectErr := execToUse.Execute(ctx, target, "inspect-"+containerName, inspectScript, &inspectBuf)
	if inspectErr != nil {
		return nil, fmt.Errorf("failed to inspect container %q on %s: %w", containerName, serverName, inspectErr)
	}
	if inspectRes != nil && !inspectRes.Success {
		return nil, fmt.Errorf("docker inspect %q failed: %v", containerName, inspectRes.Error)
	}

	info, err := docker.ParseInspectJSON(inspectBuf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to parse inspect metadata for %q: %w", containerName, err)
	}

	var (
		backupPaths         []string
		composePath         string
		isCompose           = info.IsCompose()
		isDatabase          = info.IsDatabase()
		tempDumpPath        string
		tempFilesToClean    []string
	)

	// Clean up any temporary files created on target host upon exit
	defer func() {
		if len(tempFilesToClean) > 0 {
			var rmParts []string
			for _, f := range tempFilesToClean {
				rmParts = append(rmParts, fmt.Sprintf("%q", f))
			}
			cleanScript := fmt.Sprintf("rm -f %s >/dev/null 2>&1 || true", strings.Join(rmParts, " "))
			_, _ = execToUse.Execute(context.Background(), target, "cleanup-temp", cleanScript, io.Discard)
		}
	}()

	// 2. Resolve Service Configuration
	if isCompose {
		composeDir := info.ComposeWorkingDir()
		if composeDir != "" {
			_, _ = fmt.Fprintf(consoleOut, "  -> Detected Docker Compose project at %q\n", composeDir)
			backupPaths = append(backupPaths, composeDir)
			composePath = filepath.Join(composeDir, "compose.yaml")
		}
		// Also include external bind mounts if any
		for _, m := range info.Mounts {
			if m.Type == "bind" && m.Source != "" {
				if composeDir == "" || !strings.HasPrefix(m.Source, composeDir) {
					backupPaths = append(backupPaths, m.Source)
				}
			}
		}
	} else {
		// Standalone container: reverse-translate to compose.yaml
		_, _ = fmt.Fprintf(consoleOut, "  -> Standalone container detected, reverse-compiling compose.yaml (name: %q)...\n", finalName)
		yamlStr, genErr := docker.GenerateComposeYAML(info, opts.AliasName)
		if genErr != nil {
			return nil, fmt.Errorf("failed to generate compose.yaml: %w", genErr)
		}

		projectDir := fmt.Sprintf("/var/lib/opspulse/containers/%s", finalName)
		composeFile := filepath.Join(projectDir, "compose.yaml")
		composePath = composeFile

		// Write generated compose.yaml on target host
		writeScript := fmt.Sprintf("mkdir -p %q && cat << 'EOF' > %q\n%s\nEOF\n", projectDir, composeFile, yamlStr)
		writeRes, writeErr := execToUse.Execute(ctx, target, "write-compose-"+finalName, writeScript, io.Discard)
		if writeErr != nil || (writeRes != nil && !writeRes.Success) {
			return nil, fmt.Errorf("failed to write generated compose.yaml on target %s: %v", serverName, writeErr)
		}

		backupPaths = append(backupPaths, projectDir)

		// Collect host bind mounts
		for _, m := range info.Mounts {
			if m.Type == "bind" && m.Source != "" {
				backupPaths = append(backupPaths, m.Source)
			}
		}
	}

	// 3. Handle Database Hot Dump if applicable
	if isDatabase {
		engine := info.DatabaseEngine()
		dumpFileName := docker.DumpFileName(finalName)
		tempDumpPath = fmt.Sprintf("/tmp/opspulse-dumps/%s", dumpFileName)
		tempFilesToClean = append(tempFilesToClean, tempDumpPath)

		_, _ = fmt.Fprintf(consoleOut, "  -> Database container detected (%s), creating online hot dump at %s...\n", engine, tempDumpPath)
		dumpScript, dumpScriptErr := docker.BuildDumpScript(engine, containerName, tempDumpPath)
		if dumpScriptErr != nil {
			return nil, fmt.Errorf("failed to build dump script: %w", dumpScriptErr)
		}

		dumpRes, dumpErr := execToUse.Execute(ctx, target, "dump-"+finalName, dumpScript, consoleOut)
		if dumpErr != nil || (dumpRes != nil && !dumpRes.Success) {
			return nil, fmt.Errorf("database dump failed for container %q: %v", containerName, dumpErr)
		}

		backupPaths = append(backupPaths, tempDumpPath)
	}

	// 4. Inherit repository backend and credentials
	backend, env := r.resolveInheritedBackend(finalName)
	if backend == "" {
		return nil, fmt.Errorf("no backup repository configured in backups.yaml and RESTIC_REPOSITORY is not set")
	}

	cleanPaths := dedupPaths(backupPaths)
	_, _ = fmt.Fprintf(consoleOut, "  -> Target paths to back up: %s\n", strings.Join(cleanPaths, ", "))

	// 5. Execute backup
	job := Job{
		Name:        finalName,
		Server:      serverName,
		Paths:       cleanPaths,
		Backend:     backend,
		Env:         env,
		Tags:        []string{"container", finalName},
		Description: fmt.Sprintf("Container backup for %s on %s", containerName, serverName),
	}

	runRecord, runErr := r.Run(ctx, job, consoleOut)
	if runErr != nil {
		return nil, runErr
	}

	// 6. Automatically persist configuration on success
	if runRecord != nil && runRecord.Status == "success" {
		if r.backupStore != nil {
			_ = r.backupStore.Save(job)
		}
		if r.assetStore != nil {
			assetType := asset.TypeDockerCompose
			if isDatabase {
				assetType = asset.TypeDatabase
			}
			newAsset := asset.Asset{
				ID:          finalName,
				Type:        assetType,
				Source:      composePath,
				Description: fmt.Sprintf("Auto-registered container %s on %s", containerName, serverName),
			}
			if isDatabase {
				newAsset.Engine = info.DatabaseEngine()
				newAsset.Container = containerName
			}
			_ = r.assetStore.Save(newAsset)
		}
	}

	snapshotID := ""
	if runRecord != nil {
		snapshotID = runRecord.SnapshotID
	}

	return &ContainerBackupResult{
		RunRecord:    runRecord,
		JobName:      finalName,
		ServerName:   serverName,
		SnapshotID:   snapshotID,
		ComposePath:  composePath,
		IsCompose:    isCompose,
		IsDatabase:   isDatabase,
		DatabaseDump: tempDumpPath,
		Paths:        cleanPaths,
	}, nil
}

func (r *Runner) resolveInheritedBackend(jobName string) (string, map[string]string) {
	if r.backupStore != nil {
		// Check for existing job
		if existing, err := r.backupStore.Get(jobName); err == nil && existing.Backend != "" {
			return existing.Backend, existing.Env
		}

		// Fallback to any existing configured job
		allJobs, err := r.backupStore.List()
		if err == nil && len(allJobs) > 0 {
			for _, j := range allJobs {
				if j.Backend != "" {
					return j.Backend, j.Env
				}
			}
		}
	}

	// Fallback to environment variables
	if envRepo := os.Getenv("RESTIC_REPOSITORY"); envRepo != "" {
		envMap := make(map[string]string)
		if pw := os.Getenv("RESTIC_PASSWORD"); pw != "" {
			envMap["RESTIC_PASSWORD"] = pw
		}
		return envRepo, envMap
	}

	return "", nil
}

func dedupPaths(paths []string) []string {
	seen := make(map[string]bool)
	var res []string
	for _, p := range paths {
		clean := filepath.Clean(strings.TrimSpace(p))
		if clean != "" && !seen[clean] {
			seen[clean] = true
			res = append(res, p)
		}
	}
	return res
}
