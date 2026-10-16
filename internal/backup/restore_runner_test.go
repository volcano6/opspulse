package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/storage"
)

type restoreMockExecutor struct {
	tasksExecuted []string
	autostartFail bool
}

func (m *restoreMockExecutor) Execute(_ context.Context, target executor.Target, taskName string, _ string, output io.Writer) (*executor.Result, error) {
	m.tasksExecuted = append(m.tasksExecuted, taskName)

	if strings.HasPrefix(taskName, "snapshots-") {
		snapshotJSON := `[{"id":"snap-abcdef1234567890","short_id":"snap-abc1","time":"2026-09-01T10:00:00Z"}]`
		if output != nil {
			_, _ = io.WriteString(output, snapshotJSON)
		}
	}

	if m.autostartFail && strings.HasPrefix(taskName, "autostart-") {
		return &executor.Result{
			ServerName: target.Name,
			Template:   taskName,
			Success:    false,
			ExitCode:   127,
			Duration:   50 * time.Millisecond,
		}, fmt.Errorf("exit status 127: compose engine not found")
	}

	return &executor.Result{
		ServerName: target.Name,
		Template:   taskName,
		Success:    true,
		ExitCode:   0,
		Duration:   500 * time.Millisecond,
	}, nil
}

func (m *restoreMockExecutor) Test(_ context.Context, _ executor.Target) (time.Duration, string, error) {
	return 10 * time.Millisecond, "Linux", nil
}

// stepStatus returns the recorded outcome of one restore step.
func stepStatus(t *testing.T, result *RestoreResult, kind string) RestoreStep {
	t.Helper()
	for _, step := range result.Steps {
		if step.Kind == kind {
			return step
		}
	}
	t.Fatalf("restore result has no %q step: %+v", kind, result.Steps)
	return RestoreStep{}
}

// A cross-machine restore of plain files (no container manifest) must not start
// anything: the default is files-only unless a manifest drives the migration.
func TestRestoreRunner_Run_PlainFilesDoNotStartByDefault(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	restoreRepo := storage.NewRestoreRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

	_ = serverStore.Save(server.Server{Name: "vps-01", Host: "10.0.0.1", User: "root"})
	_ = serverStore.Save(server.Server{Name: "vps-02", Host: "10.0.0.2", User: "root"})

	job := Job{
		Name:    "my-app",
		Server:  "vps-01",
		Paths:   []string{"/var/lib/opspulse/containers/my-app"},
		Backend: "/mnt/repo",
	}
	_ = backupStore.Save(job)

	mockExec := &restoreMockExecutor{}
	runner := NewRestoreRunner(mockExec, serverStore, restoreRepo, backupStore, assetStore)

	opts := RestoreOptions{
		SnapshotID:   "snap-abcdef1234567890",
		TargetServer: "vps-02",
	}

	var buf bytes.Buffer
	record, err := runner.Run(context.Background(), job, opts, &buf)
	if err != nil {
		t.Fatalf("RestoreRunner.Run() error = %v", err)
	}

	if record.Status != "success" {
		t.Errorf("record.Status = %q, want success", record.Status)
	}

	// The read-only manifest probe is how the scenario is decided, so it is
	// expected; actually starting or importing something is not.
	for _, task := range mockExec.tasksExecuted {
		switch {
		case strings.HasPrefix(task, "autostart-"),
			strings.HasPrefix(task, "compose-up-"),
			strings.HasPrefix(task, "restore-vol-"):
			t.Errorf("plain file restore must not touch container services, ran: %v", mockExec.tasksExecuted)
		}
	}

	if got := stepStatus(t, record, RestoreStepFiles); got.Status != RestoreStepSuccess {
		t.Errorf("files step = %+v, want success", got)
	}
	if got := stepStatus(t, record, RestoreStepCompose); got.Status != RestoreStepSkipped {
		t.Errorf("compose step = %+v, want skipped", got)
	}
	if len(record.Pending) == 0 {
		t.Error("a restore that left the services stopped must list the follow-up")
	}
}

// The same-host default is the other half of the policy: putting files back on
// their original server must not restart the services that own them.
func TestRestoreRunner_Run_SameHostDoesNotStartByDefault(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	restoreRepo := storage.NewRestoreRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

	_ = serverStore.Save(server.Server{Name: "vps-01", Host: "10.0.0.1", User: "root"})
	_ = serverStore.Save(server.Server{Name: "vps-02", Host: "10.0.0.2", User: "root"})

	job := Job{
		Name:    "my-app",
		Server:  "vps-01",
		Paths:   []string{"/data"},
		Backend: "/mnt/repo",
	}

	mockExec := &restoreMockExecutor{}
	runner := NewRestoreRunner(mockExec, serverStore, restoreRepo, backupStore, assetStore)

	opts := RestoreOptions{
		SnapshotID: "snap-abcdef1234567890",
	}

	var buf bytes.Buffer
	record, err := runner.Run(context.Background(), job, opts, &buf)
	if err != nil {
		t.Fatalf("RestoreRunner.Run() error = %v", err)
	}

	if record.Status != "success" {
		t.Errorf("record.Status = %q, want success", record.Status)
	}

	for _, task := range mockExec.tasksExecuted {
		if strings.HasPrefix(task, "autostart-") || strings.HasPrefix(task, "read-manifest-") || strings.HasPrefix(task, "compose-up-") {
			t.Errorf("same-host restore must not start services, ran: %v", mockExec.tasksExecuted)
		}
	}
	if got := stepStatus(t, record, RestoreStepCompose); got.Status != RestoreStepSkipped {
		t.Errorf("compose step = %+v, want skipped", got)
	}
}

// --start is the explicit opt-in: it restores files and then runs the generic
// auto-start, which is the behaviour every script written before the
// scenario-aware default relied on.
func TestRestoreRunner_Run_ExplicitStartRunsGenericAutoStart(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	restoreRepo := storage.NewRestoreRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

	_ = serverStore.Save(server.Server{Name: "vps-01", Host: "10.0.0.1", User: "root"})
	_ = serverStore.Save(server.Server{Name: "vps-02", Host: "10.0.0.2", User: "root"})

	job := Job{
		Name:    "my-app",
		Server:  "vps-01",
		Paths:   []string{"/data"},
		Backend: "/mnt/repo",
	}

	mockExec := &restoreMockExecutor{}
	runner := NewRestoreRunner(mockExec, serverStore, restoreRepo, backupStore, assetStore)

	var buf bytes.Buffer
	record, err := runner.Run(context.Background(), job, RestoreOptions{
		SnapshotID:  "snap-abcdef1234567890",
		StartPolicy: StartPolicyStart,
	}, &buf)
	if err != nil {
		t.Fatalf("RestoreRunner.Run() error = %v", err)
	}
	if record.Status != "success" {
		t.Errorf("record.Status = %q, want success", record.Status)
	}

	foundAutostart := false
	for _, task := range mockExec.tasksExecuted {
		if strings.HasPrefix(task, "autostart-") {
			foundAutostart = true
		}
	}
	if !foundAutostart {
		t.Errorf("--start must run the generic auto-start, executed tasks: %v", mockExec.tasksExecuted)
	}
	if got := stepStatus(t, record, RestoreStepCompose); got.Status != RestoreStepSuccess {
		t.Errorf("compose step = %+v, want success", got)
	}
	if got := stepStatus(t, record, RestoreStepVolumes); got.Status != RestoreStepSkipped {
		t.Errorf("volumes step = %+v, want skipped (no manifest)", got)
	}
}

// --no-start restores files only and never even probes for a manifest.
func TestRestoreRunner_Run_NoStartSkipsEveryContainerStep(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	restoreRepo := storage.NewRestoreRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

	_ = serverStore.Save(server.Server{Name: "vps-01", Host: "10.0.0.1", User: "root"})
	_ = serverStore.Save(server.Server{Name: "vps-02", Host: "10.0.0.2", User: "root"})

	job := Job{
		Name:    "my-app",
		Server:  "vps-01",
		Paths:   []string{"/data"},
		Backend: "/mnt/repo",
	}

	mockExec := &restoreMockExecutor{}
	runner := NewRestoreRunner(mockExec, serverStore, restoreRepo, backupStore, assetStore)

	var buf bytes.Buffer
	record, err := runner.Run(context.Background(), job, RestoreOptions{
		SnapshotID:   "snap-abcdef1234567890",
		TargetServer: "vps-02",
		StartPolicy:  StartPolicyNoStart,
	}, &buf)
	if err != nil {
		t.Fatalf("RestoreRunner.Run() error = %v", err)
	}
	if record.Status != "success" {
		t.Errorf("record.Status = %q, want success", record.Status)
	}

	for _, task := range mockExec.tasksExecuted {
		if strings.HasPrefix(task, "autostart-") || strings.HasPrefix(task, "read-manifest-") {
			t.Errorf("--no-start must not run container tasks, got: %v", mockExec.tasksExecuted)
		}
	}
	for _, kind := range []string{RestoreStepVolumes, RestoreStepDatabase, RestoreStepCompose} {
		step := stepStatus(t, record, kind)
		if step.Status != RestoreStepSkipped {
			t.Errorf("%s step = %+v, want skipped", kind, step)
		}
	}
	if !strings.Contains(stepStatus(t, record, RestoreStepCompose).Detail, "--no-start") {
		t.Errorf("skipped compose step must name the flag that skipped it: %+v", stepStatus(t, record, RestoreStepCompose))
	}
}

func TestRestoredPath(t *testing.T) {
	tests := []struct {
		target   string
		original string
		expected string
	}{
		{"/", "/var/lib/app", "/var/lib/app"},
		{"", "/var/lib/app", "/var/lib/app"},
		{"/mnt/restore", "/var/lib/app", "/mnt/restore/var/lib/app"},
		{"/mnt/restore/", "/var/lib/app", "/mnt/restore/var/lib/app"},
		{"/opt/dest", "/tmp/dump.sql.gz", "/opt/dest/tmp/dump.sql.gz"},
	}

	for _, tt := range tests {
		got := RestoredPath(tt.target, tt.original)
		if got != tt.expected {
			t.Errorf("RestoredPath(%q, %q) = %q, want %q", tt.target, tt.original, got, tt.expected)
		}
	}
}

func TestRestoreRunner_Run_WrongSnapshotRejected(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	restoreRepo := storage.NewRestoreRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

	_ = serverStore.Save(server.Server{Name: "vps-01", Host: "10.0.0.1", User: "root"})

	job := Job{
		Name:    "my-app",
		Server:  "vps-01",
		Paths:   []string{"/var/lib/opspulse/containers/my-app"},
		Backend: "/mnt/repo",
	}

	mockExec := &restoreMockExecutor{}
	runner := NewRestoreRunner(mockExec, serverStore, restoreRepo, backupStore, assetStore)

	opts := RestoreOptions{
		SnapshotID: "wrong-snapshot-from-other-job",
	}

	var buf bytes.Buffer
	_, err = runner.Run(context.Background(), job, opts, &buf)
	if err == nil {
		t.Fatal("expected error when restoring snapshot belonging to another job, got nil")
	}
	if !strings.Contains(err.Error(), "does not belong to job") {
		t.Errorf("error message should mention snapshot mismatch: %v", err)
	}
}

func TestRestoreRunner_Run_AutoStartExit127_MarksPartialStatus(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	restoreRepo := storage.NewRestoreRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

	_ = serverStore.Save(server.Server{Name: "vps-01", Host: "10.0.0.1", User: "root"})

	job := Job{
		Name:    "web-app",
		Server:  "vps-01",
		Paths:   []string{"/var/lib/opspulse/containers/web-app"},
		Backend: "/mnt/repo",
	}
	_ = backupStore.Save(job)

	mockExec := &restoreMockExecutor{autostartFail: true}
	runner := NewRestoreRunner(mockExec, serverStore, restoreRepo, backupStore, assetStore)

	opts := RestoreOptions{
		SnapshotID:  "snap-abcdef1234567890",
		StartPolicy: StartPolicyStart,
	}

	var buf bytes.Buffer
	record, err := runner.Run(context.Background(), job, opts, &buf)
	if err == nil {
		t.Fatal("expected error when autostart fails with exit 127, got nil")
	}

	if record.Status != "partial" {
		t.Errorf("record.Status = %q, want 'partial'", record.Status)
	}
	if !strings.Contains(record.ErrorMessage, "container auto-start failed") {
		t.Errorf("expected error message to mention container auto-start failed, got: %s", record.ErrorMessage)
	}

	// Verify database record is also updated to "partial"
	runs, repoErr := restoreRepo.ListRuns(context.Background(), "", 10)
	if repoErr != nil {
		t.Fatalf("restoreRepo.ListRuns() error: %v", repoErr)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run in repo, got %d", len(runs))
	}
	if runs[0].Status != "partial" {
		t.Errorf("repo record status = %q, want 'partial'", runs[0].Status)
	}
}

// manifestRestoreExecutor serves manifest bodies for the read-manifest probe
// and records every generated script, so tests can assert on the commands
// handed to the target instead of on internal state.
type manifestRestoreExecutor struct {
	manifests map[string]string // absolute manifest path -> YAML body
	scripts   map[string]string // task name -> generated script
	executed  []string
}

func extractCatPath(script string) string {
	i := strings.IndexByte(script, '\'')
	if i < 0 {
		return ""
	}
	rest := script[i+1:]
	j := strings.IndexByte(rest, '\'')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func (m *manifestRestoreExecutor) Execute(_ context.Context, target executor.Target, taskName, script string, output io.Writer) (*executor.Result, error) {
	m.executed = append(m.executed, taskName)
	if m.scripts == nil {
		m.scripts = make(map[string]string)
	}
	m.scripts[taskName] = script

	switch {
	case strings.HasPrefix(taskName, "read-manifest-"):
		if body, ok := m.manifests[extractCatPath(script)]; ok && output != nil {
			_, _ = io.WriteString(output, body)
		}
	case taskName == "check-mount":
		// Every probed external mount is reported absent, which is the signal
		// the restore runner surfaces as a warning.
		if output != nil {
			_, _ = io.WriteString(output, "MISSING")
		}
	case strings.HasPrefix(taskName, "snapshots-"):
		if output != nil {
			_, _ = io.WriteString(output, `[{"id":"snap-abcdef1234567890","short_id":"snap-abc1","time":"2026-09-01T10:00:00Z"}]`)
		}
	}

	return &executor.Result{
		ServerName: target.Name,
		Template:   taskName,
		Success:    true,
		ExitCode:   0,
		Duration:   time.Millisecond,
	}, nil
}

func (m *manifestRestoreExecutor) Test(_ context.Context, _ executor.Target) (time.Duration, string, error) {
	return 10 * time.Millisecond, "Linux", nil
}

func (m *manifestRestoreExecutor) hasTaskPrefix(prefix string) bool {
	for _, task := range m.executed {
		if strings.HasPrefix(task, prefix) {
			return true
		}
	}
	return false
}

const restoreTestManifest = `format_version: 1
app: blog
compose_file: compose.yaml
volumes:
    - original_name: blog-uploads
      archive: volumes/blog-uploads/data.tar
      target: /var/lib/app/uploads
`

func newRestoreTestRunner(t *testing.T, exec executor.Executor) (*RestoreRunner, *asset.Store, Job) {
	t.Helper()
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	_ = serverStore.Save(server.Server{Name: "vps-01", Host: "10.0.0.1", User: "root"})
	_ = serverStore.Save(server.Server{Name: "vps-02", Host: "10.0.0.2", User: "root"})

	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))
	job := Job{Name: "blog", Server: "vps-01", Paths: []string{"/opt/blog"}, Backend: "/mnt/repo"}

	runner := NewRestoreRunner(exec, serverStore, storage.NewRestoreRepo(db), NewStore(filepath.Join(tmpDir, "backups.yaml")), assetStore)
	return runner, assetStore, job
}

// Old snapshots keep manifest.yaml and the volume archives directly in the
// project directory; restore must still find and consume them.
func TestRestoreRunner_Manifest_LegacyLayoutStillRestorable(t *testing.T) {
	mock := &manifestRestoreExecutor{manifests: map[string]string{
		"/opt/blog/manifest.yaml": restoreTestManifest,
	}}
	runner, _, job := newRestoreTestRunner(t, mock)

	var buf bytes.Buffer
	record, err := runner.Run(context.Background(), job, RestoreOptions{
		SnapshotID:   "snap-abcdef1234567890",
		TargetServer: "vps-02",
	}, &buf)
	if err != nil {
		t.Fatalf("RestoreRunner.Run() error = %v", err)
	}
	if record.Status != "success" {
		t.Errorf("record.Status = %q, want success", record.Status)
	}

	if !mock.hasTaskPrefix("compose-up-") {
		t.Fatalf("legacy manifest did not drive a compose start; executed: %v", mock.executed)
	}
	if mock.hasTaskPrefix("autostart-") {
		t.Errorf("manifest-driven restore must not also run the autostart fallback: %v", mock.executed)
	}

	// Archive paths stay relative to the project directory for legacy layouts.
	importScript := mock.scripts["restore-vol-blog-uploads"]
	if !strings.Contains(importScript, "'/opt/blog/volumes/blog-uploads':/src:ro") {
		t.Errorf("legacy volume archive path not used: %q", importScript)
	}
	// The manifest name itself is read from the legacy location.
	if _, ok := mock.manifests["/opt/blog/manifest.yaml"]; !ok {
		t.Fatal("test fixture not wired")
	}
}

// Snapshots produced after the scratch-namespace change keep their manifest
// and archives under ".opspulse/".
func TestRestoreRunner_Manifest_ScratchLayoutRestorable(t *testing.T) {
	mock := &manifestRestoreExecutor{manifests: map[string]string{
		"/opt/blog/.opspulse/manifest.yaml": restoreTestManifest,
	}}
	runner, _, job := newRestoreTestRunner(t, mock)

	var buf bytes.Buffer
	record, err := runner.Run(context.Background(), job, RestoreOptions{
		SnapshotID:   "snap-abcdef1234567890",
		TargetServer: "vps-02",
	}, &buf)
	if err != nil {
		t.Fatalf("RestoreRunner.Run() error = %v", err)
	}
	if record.Status != "success" {
		t.Errorf("record.Status = %q, want success", record.Status)
	}
	if !mock.hasTaskPrefix("compose-up-") {
		t.Fatalf("scratch manifest did not drive a compose start; executed: %v", mock.executed)
	}
	if mock.hasTaskPrefix("autostart-") {
		t.Errorf("manifest-driven restore must not also run the autostart fallback: %v", mock.executed)
	}

	importScript := mock.scripts["restore-vol-blog-uploads"]
	if !strings.Contains(importScript, "'/opt/blog/.opspulse/volumes/blog-uploads':/src:ro") {
		t.Errorf("scratch volume archive path not used: %q", importScript)
	}

	startScript := mock.scripts["compose-up-blog"]
	if !strings.Contains(startScript, "cd '/opt/blog'") {
		t.Errorf("compose must run from the project directory, not the scratch directory: %q", startScript)
	}
}

// A manifest that names a database without a logical dump cannot be imported
// automatically: the step is reported as manual (with a follow-up) instead of
// being silently treated as done, and the aggregate stays success because the
// data question is the operator's call, not a failed step.
func TestRestoreRunner_Manifest_DatabaseWithoutDumpIsManual(t *testing.T) {
	const manifestWithBareDatabase = `format_version: 1
app: blog
compose_file: compose.yaml
database:
    engine: mysql
    container: blog-db
`
	mock := &manifestRestoreExecutor{manifests: map[string]string{
		"/opt/blog/.opspulse/manifest.yaml": manifestWithBareDatabase,
	}}
	runner, _, job := newRestoreTestRunner(t, mock)

	var buf bytes.Buffer
	result, err := runner.Run(context.Background(), job, RestoreOptions{
		SnapshotID:   "snap-abcdef1234567890",
		TargetServer: "vps-02",
	}, &buf)
	if err != nil {
		t.Fatalf("RestoreRunner.Run() error = %v", err)
	}

	if result.Status != "success" {
		t.Errorf("record.Status = %q, want success", result.Status)
	}
	databaseStep := stepStatus(t, result, RestoreStepDatabase)
	if databaseStep.Status != RestoreStepManual {
		t.Errorf("database step = %+v, want manual", databaseStep)
	}
	if databaseStep.NextStep == "" {
		t.Error("a manual step must tell the operator what to do")
	}
	if mock.hasTaskPrefix("import-db-") {
		t.Errorf("a database without a dump must not be imported: %v", mock.executed)
	}
	if len(result.Pending) == 0 {
		t.Error("a manual step must appear in the outstanding work")
	}
}

// A manifest that names a different application must never drive the restore.
// With an explicit --start the engine falls back to the generic auto-start (the
// pre-08a behaviour); without it, no service is touched at all.
func TestRestoreRunner_Manifest_MismatchFallsBackInsteadOfStarting(t *testing.T) {
	const otherAppManifest = `format_version: 1
app: other-app
compose_file: compose.yaml
`
	mock := &manifestRestoreExecutor{manifests: map[string]string{
		"/opt/blog/.opspulse/manifest.yaml": otherAppManifest,
	}}
	runner, _, job := newRestoreTestRunner(t, mock)

	var buf bytes.Buffer
	if _, err := runner.Run(context.Background(), job, RestoreOptions{
		SnapshotID:   "snap-abcdef1234567890",
		TargetServer: "vps-02",
		StartPolicy:  StartPolicyStart,
	}, &buf); err != nil {
		t.Fatalf("RestoreRunner.Run() error = %v", err)
	}

	if mock.hasTaskPrefix("compose-up-") {
		t.Errorf("a manifest for another app must not start containers: %v", mock.executed)
	}
	if mock.hasTaskPrefix("restore-vol-") {
		t.Errorf("a manifest for another app must not import volumes: %v", mock.executed)
	}
	if !mock.hasTaskPrefix("autostart-") {
		t.Errorf("mismatched manifest should fall back to auto-start, executed: %v", mock.executed)
	}

	// The operator must be told which manifest was rejected and for which job.
	out := buf.String()
	for _, want := range []string{`"other-app"`, `"blog"`} {
		if !strings.Contains(out, want) {
			t.Errorf("warning does not name %s: %s", want, out)
		}
	}
	if !strings.Contains(out, "Skipping manifest-driven restore") {
		t.Errorf("warning does not explain the skip: %s", out)
	}
}

// `ops restore run --dry-run --asset <id>` must build a `restic ls` command
// that restic actually accepts (no --include).
func TestRestoreRunner_DryRunAsset_UsesResticSupportedFlags(t *testing.T) {
	mock := &manifestRestoreExecutor{}
	runner, assetStore, job := newRestoreTestRunner(t, mock)
	if err := assetStore.Save(asset.Asset{ID: "blog", Type: asset.TypeDockerCompose, Source: "/opt/blog"}); err != nil {
		t.Fatalf("assetStore.Save() error: %v", err)
	}

	var buf bytes.Buffer
	record, err := runner.Run(context.Background(), job, RestoreOptions{
		SnapshotID: "snap-abcdef1234567890",
		AssetID:    "blog",
		DryRun:     true,
	}, &buf)
	if err != nil {
		t.Fatalf("RestoreRunner.Run() error = %v", err)
	}
	if record.Status != "dry-run" {
		t.Errorf("record.Status = %q, want dry-run", record.Status)
	}

	script := mock.scripts["restore-dryrun-blog"]
	if script == "" {
		t.Fatalf("dry-run script was not executed; executed: %v", mock.executed)
	}
	if strings.Contains(script, "--include") {
		t.Errorf("restic ls does not support --include: %q", script)
	}
	if !strings.Contains(script, "--path '/opt/blog'") {
		t.Errorf("dry-run did not pass the asset path via --path: %q", script)
	}
}

// A host runtime dependency such as /sys/fs/cgroup must be recognized as a
// system mount and reported when it is absent on the restore target. The
// decision is delegated to docker.IsSystemMount so that these paths cannot
// drift apart from the backup side.
func TestRestoreRunner_SystemMountWarningCoversAllRuntimePaths(t *testing.T) {
	const manifestWithSystemMount = `format_version: 1
app: blog
compose_file: compose.yaml
external_mounts:
    - source: /sys/fs/cgroup
      target: /sys/fs/cgroup
      required: true
      reason: bind_mount
`
	mock := &manifestRestoreExecutor{manifests: map[string]string{
		"/opt/blog/.opspulse/manifest.yaml": manifestWithSystemMount,
	}}
	runner, _, job := newRestoreTestRunner(t, mock)

	var buf bytes.Buffer
	if _, err := runner.Run(context.Background(), job, RestoreOptions{
		SnapshotID:   "snap-abcdef1234567890",
		TargetServer: "vps-02",
	}, &buf); err != nil {
		t.Fatalf("RestoreRunner.Run() error = %v", err)
	}

	if !mock.hasTaskPrefix("compose-up-") {
		t.Fatalf("manifest did not drive a compose start; executed: %v", mock.executed)
	}
	out := buf.String()
	if !strings.Contains(out, "/sys/fs/cgroup") || !strings.Contains(out, "is missing on target") {
		t.Errorf("missing system mount was not reported: %s", out)
	}
}

// The mismatch warning is not enough on its own: with the scenario-aware
// default (no --start) a manifest that belongs to another app must leave every
// service alone.
func TestRestoreRunner_Manifest_MismatchDoesNotStartWithoutExplicitStart(t *testing.T) {
	const otherAppManifest = `format_version: 1
app: other-app
compose_file: compose.yaml
`
	mock := &manifestRestoreExecutor{manifests: map[string]string{
		"/opt/blog/.opspulse/manifest.yaml": otherAppManifest,
	}}
	runner, _, job := newRestoreTestRunner(t, mock)

	var buf bytes.Buffer
	result, err := runner.Run(context.Background(), job, RestoreOptions{
		SnapshotID:   "snap-abcdef1234567890",
		TargetServer: "vps-02",
	}, &buf)
	if err != nil {
		t.Fatalf("RestoreRunner.Run() error = %v", err)
	}

	for _, task := range mock.executed {
		if strings.HasPrefix(task, "compose-up-") || strings.HasPrefix(task, "autostart-") || strings.HasPrefix(task, "restore-vol-") {
			t.Errorf("a manifest for another app must not start or import anything, ran: %v", mock.executed)
		}
	}
	if got := stepStatus(t, result, RestoreStepCompose); got.Status != RestoreStepSkipped {
		t.Errorf("compose step = %+v, want skipped", got)
	}
	if len(result.Warnings) == 0 {
		t.Error("the rejected manifest must still be reported as a warning")
	}
}

// TestRestoreRunner_RejectsUnsafeAlias covers the restore boundary: `--as` is
// passed straight into generated shell scripts, so it must be validated before
// anything reaches the target host.
func TestRestoreRunner_RejectsUnsafeAlias(t *testing.T) {
	unsafe := []string{`$(id)`, "`id`", "a;id", "a'b", "../escape", "a/b", ".hidden", "数据", "a b"}
	for _, alias := range unsafe {
		t.Run(alias, func(t *testing.T) {
			mock := &manifestRestoreExecutor{}
			runner, _, job := newRestoreTestRunner(t, mock)

			var buf bytes.Buffer
			_, err := runner.Run(context.Background(), job, RestoreOptions{
				SnapshotID: "snap-abcdef1234567890",
				AliasName:  alias,
			}, &buf)
			if err == nil {
				t.Fatalf("RestoreRunner.Run() accepted unsafe alias %q", alias)
			}
			if len(mock.executed) != 0 {
				t.Errorf("unsafe alias %q reached the target host: %v", alias, mock.executed)
			}
		})
	}

	t.Run("safe alias is quoted verbatim", func(t *testing.T) {
		mock := &manifestRestoreExecutor{}
		runner, _, job := newRestoreTestRunner(t, mock)

		var buf bytes.Buffer
		if _, err := runner.Run(context.Background(), job, RestoreOptions{
			SnapshotID:  "snap-abcdef1234567890",
			AliasName:   "blog-v2",
			StartPolicy: StartPolicyStart,
		}, &buf); err != nil {
			t.Fatalf("RestoreRunner.Run() error = %v", err)
		}
		script := mock.scripts["autostart-blog"]
		if !strings.Contains(script, `export COMPOSE_PROJECT_NAME='blog-v2'`) {
			t.Errorf("autostart did not quote the alias: %q", script)
		}
	})
}
