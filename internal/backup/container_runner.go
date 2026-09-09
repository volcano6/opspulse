package backup

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/docker"
	"github.com/volcano6/opspulse/internal/shellquote"
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
	inspectScript := fmt.Sprintf("docker inspect %s", shellquote.Quote(containerName))
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
		backupPaths      []string
		composePath      string
		isCompose        = info.IsCompose()
		isDatabase       = info.IsDatabase()
		tempDumpPath     string
		tempFilesToClean []string
		projectDir       string
	)

	// Clean up any temporary files created on target host upon exit
	defer func() {
		if len(tempFilesToClean) > 0 {
			var rmParts []string
			for _, f := range tempFilesToClean {
				rmParts = append(rmParts, shellquote.Quote(f))
			}
			cleanScript := fmt.Sprintf("rm -rf %s >/dev/null 2>&1 || true", strings.Join(rmParts, " "))
			_, _ = execToUse.Execute(context.Background(), target, "cleanup-temp", cleanScript, io.Discard)
		}
	}()

	// 2. Resolve Service Configuration and Project Layout
	if isCompose {
		projectDir = info.ComposeWorkingDir()
		if projectDir == "" {
			projectDir = fmt.Sprintf("/var/lib/opspulse/containers/%s", finalName)
		}
		_, _ = fmt.Fprintf(consoleOut, "  -> Detected Docker Compose project at %q\n", projectDir)
		composePath = path.Join(projectDir, "compose.yaml")
	} else {
		projectDir = fmt.Sprintf("/var/lib/opspulse/containers/%s", finalName)
		composePath = path.Join(projectDir, "compose.yaml")
	}

	manifest := &docker.ContainerManifest{
		FormatVersion: 1,
		App:           finalName,
		ComposeFile:   "compose.yaml",
	}

	// 3. Process Mounts: Named Volumes and Bind Mounts
	for _, m := range info.Mounts {
		if m.Type == "volume" || docker.IsNamedVolume(m) {
			volName := m.Name
			if volName == "" {
				volName = m.Source
			}
			if isDatabase && info.IsDatabaseDataDir(m.Destination) {
				// Live physical database directory is excluded in favor of logical hot dump
				continue
			}
			archiveRel := fmt.Sprintf("volumes/%s/data.tar", volName)
			archiveFull := path.Join(projectDir, archiveRel)
			manifest.Volumes = append(manifest.Volumes, docker.ManifestVolume{
				OriginalName: volName,
				Archive:      archiveRel,
				Target:       m.Destination,
			})

			_, _ = fmt.Fprintf(consoleOut, "  -> Archiving named volume %q to %s...\n", volName, archiveRel)
			exportScript := docker.BuildVolumeExportScript(volName, archiveFull)
			expRes, expErr := execToUse.Execute(ctx, target, "export-vol-"+volName, exportScript, consoleOut)
			if expErr != nil || (expRes != nil && !expRes.Success) {
				return nil, fmt.Errorf("failed to export volume %q on target %s: %v", volName, serverName, expErr)
			}
			tempFilesToClean = append(tempFilesToClean, archiveFull)
		} else if m.Type == "bind" {
			if docker.IsSystemMount(m.Source) {
				manifest.ExternalMounts = append(manifest.ExternalMounts, docker.ManifestExternalMount{
					Source:   m.Source,
					Target:   m.Destination,
					Required: true,
					Reason:   "system_mount",
				})
			} else {
				manifest.ExternalMounts = append(manifest.ExternalMounts, docker.ManifestExternalMount{
					Source:   m.Source,
					Target:   m.Destination,
					Required: true,
					Reason:   "bind_mount",
				})
				if !strings.HasPrefix(m.Source, projectDir) {
					backupPaths = append(backupPaths, m.Source)
				}
			}
		}
	}

	// 4. Handle Database Hot Dump if applicable
	if isDatabase {
		engine := info.DatabaseEngine()
		dumpFileName := docker.DumpFileName(finalName)
		dumpRel := fmt.Sprintf("dumps/%s", dumpFileName)
		dumpFull := path.Join(projectDir, dumpRel)
		tempDumpPath = dumpFull
		tempFilesToClean = append(tempFilesToClean, dumpFull)

		manifest.Database = &docker.ManifestDatabase{
			Engine:    engine,
			Container: containerName,
			Dump:      dumpRel,
		}

		_, _ = fmt.Fprintf(consoleOut, "  -> Database container detected (%s), creating online hot dump at %s...\n", engine, dumpRel)
		dumpScript, dumpScriptErr := docker.BuildDumpScript(engine, containerName, dumpFull)
		if dumpScriptErr != nil {
			return nil, fmt.Errorf("failed to build dump script: %w", dumpScriptErr)
		}

		dumpRes, dumpErr := execToUse.Execute(ctx, target, "dump-"+finalName, dumpScript, consoleOut)
		if dumpErr != nil || (dumpRes != nil && !dumpRes.Success) {
			return nil, fmt.Errorf("database dump failed for container %q: %v", containerName, dumpErr)
		}
	}

	// 5. Generate and write compose.yaml (for standalone) and manifest.yaml (all)
	if !isCompose {
		_, _ = fmt.Fprintf(consoleOut, "  -> Standalone container detected, reverse-compiling compose.yaml (name: %q)...\n", finalName)
		yamlStr, genErr := docker.GenerateComposeYAML(info, opts.AliasName)
		if genErr != nil {
			return nil, fmt.Errorf("failed to generate compose.yaml: %w", genErr)
		}

		encodedCompose := base64.StdEncoding.EncodeToString([]byte(yamlStr))
		writeScript := fmt.Sprintf("mkdir -p %s && printf '%%s' '%s' | base64 -d > %s\n",
			shellquote.Quote(projectDir), encodedCompose, shellquote.Quote(composePath))
		writeRes, writeErr := execToUse.Execute(ctx, target, "write-compose-"+finalName, writeScript, io.Discard)
		if writeErr != nil || (writeRes != nil && !writeRes.Success) {
			return nil, fmt.Errorf("failed to write generated compose.yaml on target %s: %v", serverName, writeErr)
		}
	}

	manifestYAML, mErr := docker.MarshalManifest(manifest)
	if mErr != nil {
		return nil, fmt.Errorf("failed to marshal container manifest: %w", mErr)
	}
	manifestFile := path.Join(projectDir, docker.ManifestFileName)
	encodedManifest := base64.StdEncoding.EncodeToString([]byte(manifestYAML))
	writeManifestScript := fmt.Sprintf("mkdir -p %s && printf '%%s' '%s' | base64 -d > %s\n",
		shellquote.Quote(projectDir), encodedManifest, shellquote.Quote(manifestFile))
	mRes, mErr := execToUse.Execute(ctx, target, "write-manifest-"+finalName, writeManifestScript, io.Discard)
	if mErr != nil || (mRes != nil && !mRes.Success) {
		return nil, fmt.Errorf("failed to write manifest.yaml on target %s: %v", serverName, mErr)
	}

	backupPaths = append(backupPaths, projectDir)

	// 6. Inherit repository backend and credentials
	backend, env := r.resolveInheritedBackend(finalName)
	if backend == "" {
		return nil, fmt.Errorf("no backup repository configured in backups.yaml and RESTIC_REPOSITORY is not set")
	}

	cleanPaths := dedupPaths(backupPaths)
	_, _ = fmt.Fprintf(consoleOut, "  -> Target paths to back up: %s\n", strings.Join(cleanPaths, ", "))

	// 7. Execute backup
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

	// 8. Automatically persist configuration on success
	if runRecord != nil && runRecord.Status == "success" {
		if r.backupStore != nil {
			if err := r.backupStore.Save(job); err != nil {
				_, _ = fmt.Fprintf(consoleOut, "Warning: failed to persist backup job %q to store: %v\n", job.Name, err)
			}
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
			if err := r.assetStore.Save(newAsset); err != nil {
				_, _ = fmt.Fprintf(consoleOut, "Warning: failed to persist asset %q to store: %v\n", newAsset.ID, err)
			}
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
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		clean := path.Clean(strings.ReplaceAll(trimmed, "\\", "/"))
		if clean != "" && !seen[clean] {
			seen[clean] = true
			res = append(res, clean)
		}
	}
	return res
}
