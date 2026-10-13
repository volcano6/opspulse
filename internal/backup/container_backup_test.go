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

func TestParseContainerTarget(t *testing.T) {
	tests := []struct {
		arg        string
		wantServer string
		wantCtr    string
		wantIsCtr  bool
	}{
		{"vps-1:nginx-test", "vps-1", "nginx-test", true},
		{"prod-server:blog-db", "prod-server", "blog-db", true},
		{"vps-1:my-app:extra", "vps-1", "my-app:extra", true},
		{"simple-backup-job", "", "", false},
		{":container", "", "", false},
		{"server:", "", "", false},
		{"", "", "", false},
		{"   ", "", "", false},
	}

	for _, tc := range tests {
		srv, ctr, isCtr := ParseContainerTarget(tc.arg)
		if isCtr != tc.wantIsCtr || srv != tc.wantServer || ctr != tc.wantCtr {
			t.Errorf("ParseContainerTarget(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.arg, srv, ctr, isCtr, tc.wantServer, tc.wantCtr, tc.wantIsCtr)
		}
	}
}

func TestJob_ResolveAllPaths(t *testing.T) {
	tmpDir := t.TempDir()
	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

	_ = assetStore.Save(asset.Asset{
		ID:     "blog-web",
		Type:   asset.TypeDockerCompose,
		Source: "/opt/blog",
	})
	_ = assetStore.Save(asset.Asset{
		ID:     "site-configs",
		Type:   asset.TypeDirectory,
		Source: "/etc/nginx",
	})

	job := Job{
		Name:   "full-backup",
		Server: "vps-1",
		Paths:  []string{"/var/log", "/opt/blog"}, // /opt/blog is duplicate with blog-web
		Assets: []string{"blog-web", "site-configs"},
	}

	paths, err := job.ResolveAllPaths(assetStore)
	if err != nil {
		t.Fatalf("ResolveAllPaths() error = %v", err)
	}

	if len(paths) != 3 {
		t.Fatalf("expected 3 deduplicated paths, got %d: %v", len(paths), paths)
	}

	expected := []string{"/var/log", "/opt/blog", "/etc/nginx"}
	for i, exp := range expected {
		if paths[i] != exp {
			t.Errorf("paths[%d] = %q, want %q", i, paths[i], exp)
		}
	}

	// Missing asset ID
	badJob := Job{
		Name:   "bad-job",
		Server: "vps-1",
		Assets: []string{"non-existent-asset"},
	}
	if _, err := badJob.ResolveAllPaths(assetStore); err == nil {
		t.Error("expected error for non-existent asset ID, got nil")
	}
}

func TestDedupPaths(t *testing.T) {
	input := []string{"/var/www", "/var/www/", " /etc/nginx ", "/var/www", "/tmp"}
	got := dedupPaths(input)
	if len(got) != 3 {
		t.Fatalf("expected 3 paths, got %d: %v", len(got), got)
	}
}

type dynamicMockExecutor struct {
	inspectJSON     string
	resticOutput    string
	executedScripts map[string]string
}

func (m *dynamicMockExecutor) Execute(_ context.Context, target executor.Target, taskName string, script string, output io.Writer) (*executor.Result, error) {
	if m.executedScripts == nil {
		m.executedScripts = make(map[string]string)
	}
	m.executedScripts[taskName] = script
	if strings.HasPrefix(taskName, "inspect-") {
		if output != nil && m.inspectJSON != "" {
			_, _ = io.WriteString(output, m.inspectJSON)
		}
		return &executor.Result{
			ServerName: target.Name,
			Template:   taskName,
			Success:    true,
			ExitCode:   0,
		}, nil
	}

	if strings.HasPrefix(taskName, "write-compose-") || strings.HasPrefix(taskName, "dump-") || strings.HasPrefix(taskName, "cleanup-temp") {
		return &executor.Result{
			ServerName: target.Name,
			Template:   taskName,
			Success:    true,
			ExitCode:   0,
		}, nil
	}

	if strings.HasPrefix(taskName, "backup-") {
		if output != nil && m.resticOutput != "" {
			_, _ = io.WriteString(output, m.resticOutput)
		}
		return &executor.Result{
			ServerName: target.Name,
			Template:   taskName,
			Success:    true,
			ExitCode:   0,
			Duration:   1 * time.Second,
		}, nil
	}

	return &executor.Result{
		ServerName: target.Name,
		Template:   taskName,
		Success:    true,
		ExitCode:   0,
	}, nil
}

func (m *dynamicMockExecutor) Test(_ context.Context, _ executor.Target) (time.Duration, string, error) {
	return 10 * time.Millisecond, "Linux", nil
}

func TestRunContainerBackup_Standalone_WithAlias(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	backupRepo := storage.NewBackupRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

	_ = serverStore.Save(server.Server{Name: "vps-01", Host: "10.0.0.1", User: "root"})

	// Pre-seed a job in backups.yaml so backend is inherited
	_ = backupStore.Save(Job{
		Name:    "existing-job",
		Server:  "vps-01",
		Paths:   []string{"/var/log"},
		Backend: "s3:s3.amazonaws.com/my-bucket",
		Env:     map[string]string{"RESTIC_PASSWORD": "secret-password"},
	})

	inspectJSON := `[{
		"Id": "abc12345",
		"Name": "/nginx-test",
		"Config": {
			"Image": "nginx:alpine",
			"WorkingDir": "/app"
		},
		"HostConfig": {
			"PortBindings": {"80/tcp": [{"HostIp": "0.0.0.0", "HostPort": "8080"}]},
			"Binds": ["/data/html:/usr/share/nginx/html"]
		}
	}]`

	resticOutput := `{"message_type":"summary","files_new":5,"data_added":2048,"total_duration":1.2,"snapshot_id":"snap-98765432"}`

	exec := &dynamicMockExecutor{
		inspectJSON:  inspectJSON,
		resticOutput: resticOutput,
	}

	runner := NewRunnerWithStores(exec, serverStore, backupRepo, backupStore, assetStore)

	opts := ContainerBackupOptions{
		Server:        "vps-01",
		ContainerName: "nginx-test",
		AliasName:     "nginx",
	}

	var buf bytes.Buffer
	res, err := runner.RunContainerBackup(context.Background(), opts, &buf)
	if err != nil {
		t.Fatalf("RunContainerBackup() error: %v", err)
	}

	if res.JobName != "nginx" {
		t.Errorf("res.JobName = %q, want nginx", res.JobName)
	}
	if res.SnapshotID != "snap-98765432" {
		t.Errorf("res.SnapshotID = %q, want snap-98765432", res.SnapshotID)
	}
	if res.IsCompose {
		t.Error("expected IsCompose to be false")
	}

	// Verify persistence in backupStore
	savedJob, err := backupStore.Get("nginx")
	if err != nil {
		t.Fatalf("backupStore.Get('nginx') failed: %v", err)
	}
	if savedJob.Backend != "s3:s3.amazonaws.com/my-bucket" {
		t.Errorf("savedJob.Backend = %q, want s3:s3.amazonaws.com/my-bucket", savedJob.Backend)
	}

	// Verify persistence in assetStore
	savedAsset, err := assetStore.Get("nginx")
	if err != nil {
		t.Fatalf("assetStore.Get('nginx') failed: %v", err)
	}
	if savedAsset.Type != asset.TypeDockerCompose {
		t.Errorf("savedAsset.Type = %v, want docker_compose", savedAsset.Type)
	}
	// Verify write-compose script used base64 encoding instead of vulnerable heredoc
	writeScript := exec.executedScripts["write-compose-nginx"]
	if strings.Contains(writeScript, "cat << 'EOF'") {
		t.Error("write-compose script must not use vulnerable heredoc cat << 'EOF'")
	}
	if !strings.Contains(writeScript, "base64 -d") {
		t.Error("write-compose script should decode via base64 -d")
	}
}

func TestRunContainerBackup_RejectsUnsafeAlias(t *testing.T) {
	for _, alias := range []string{"../escape", "a/b", ".hidden", "-flag", "a b", "..", "a..b"} {
		t.Run(alias, func(t *testing.T) {
			tmpDir := t.TempDir()
			db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
			if err != nil {
				t.Fatalf("storage.Open() error: %v", err)
			}
			defer func() { _ = db.Close() }()

			exec := &dynamicMockExecutor{}
			runner := NewRunnerWithStores(exec,
				server.NewStore(filepath.Join(tmpDir, "servers.yaml")),
				storage.NewBackupRepo(db),
				NewStore(filepath.Join(tmpDir, "backups.yaml")),
				asset.NewStore(filepath.Join(tmpDir, "assets.yaml")),
			)

			opts := ContainerBackupOptions{Server: "vps-01", ContainerName: "nginx-test", AliasName: alias}
			var buf bytes.Buffer
			if _, err := runner.RunContainerBackup(context.Background(), opts, &buf); err == nil {
				t.Fatalf("RunContainerBackup() accepted unsafe alias %q", alias)
			}
			if len(exec.executedScripts) != 0 {
				t.Errorf("unsafe alias %q reached the target host: %v", alias, exec.executedScripts)
			}
		})
	}
}

func TestRunContainerBackup_BindMountPathBoundary(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	backupRepo := storage.NewBackupRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

	_ = serverStore.Save(server.Server{Name: "vps-03", Host: "10.0.0.3", User: "root"})

	// Seed a job so the container backup can inherit a repository backend.
	_ = backupStore.Save(Job{
		Name:    "seed",
		Server:  "vps-03",
		Paths:   []string{"/var/log"},
		Backend: "/mnt/repo",
	})

	// Alias "nginx" makes the standalone project dir
	// /var/lib/opspulse/containers/nginx.
	const projectDir = "/var/lib/opspulse/containers/nginx"

	inspectJSON := `[{
		"Id": "aaa11111",
		"Name": "/nginx-test",
		"Config": {"Image": "nginx:alpine"},
		"HostConfig": {
			"Binds": [
				"/var/lib/opspulse/containers/nginx-other:/data/other",
				"/var/lib/opspulse/containers/nginx/sub:/data/sub",
				"/opt/appdata:/data/app"
			]
		}
	}]`

	exec := &dynamicMockExecutor{
		inspectJSON:  inspectJSON,
		resticOutput: `{"message_type":"summary","files_new":1,"data_added":1,"total_duration":0.1,"snapshot_id":"snap-boundary"}`,
	}

	runner := NewRunnerWithStores(exec, serverStore, backupRepo, backupStore, assetStore)

	var buf bytes.Buffer
	res, err := runner.RunContainerBackup(context.Background(), ContainerBackupOptions{
		Server:        "vps-03",
		ContainerName: "nginx-test",
		AliasName:     "nginx",
	}, &buf)
	if err != nil {
		t.Fatalf("RunContainerBackup() error: %v", err)
	}

	hasPath := func(p string) bool {
		for _, got := range res.Paths {
			if got == p {
				return true
			}
		}
		return false
	}

	if !hasPath(projectDir) {
		t.Errorf("res.Paths = %v, want it to contain project dir %q", res.Paths, projectDir)
	}
	// A sibling directory that merely shares the project dir's byte prefix is
	// not inside it, so it must be archived in its own right.
	if !hasPath(projectDir + "-other") {
		t.Errorf("res.Paths = %v, want sibling %q to be backed up", res.Paths, projectDir+"-other")
	}
	if !hasPath("/opt/appdata") {
		t.Errorf("res.Paths = %v, want /opt/appdata to be backed up", res.Paths)
	}
	// A genuine child is already covered by the project dir itself.
	if hasPath(projectDir + "/sub") {
		t.Errorf("res.Paths = %v, want child %q to be skipped as redundant", res.Paths, projectDir+"/sub")
	}
}

func TestRunContainerBackup_ComposeAndDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	backupRepo := storage.NewBackupRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

	_ = serverStore.Save(server.Server{Name: "vps-02", Host: "10.0.0.2", User: "root"})

	_ = backupStore.Save(Job{
		Name:    "seed",
		Server:  "vps-02",
		Paths:   []string{"/tmp"},
		Backend: "/mnt/repo",
	})

	inspectJSON := `[{
		"Id": "def67890",
		"Name": "/blog-mysql",
		"Config": {
			"Image": "mysql:8.0",
			"Labels": {
				"com.docker.compose.project": "blog",
				"com.docker.compose.service": "db",
				"com.docker.compose.project.working_dir": "/opt/blog"
			}
		},
		"HostConfig": {
			"Binds": ["/var/lib/mysql:/var/lib/mysql"]
		}
	}]`

	resticOutput := `{"message_type":"summary","files_new":10,"data_added":1048576,"total_duration":2.0,"snapshot_id":"snap-mysql123"}`

	exec := &dynamicMockExecutor{
		inspectJSON:  inspectJSON,
		resticOutput: resticOutput,
	}

	runner := NewRunnerWithStores(exec, serverStore, backupRepo, backupStore, assetStore)

	opts := ContainerBackupOptions{
		Server:        "vps-02",
		ContainerName: "blog-mysql",
	}

	var buf bytes.Buffer
	res, err := runner.RunContainerBackup(context.Background(), opts, &buf)
	if err != nil {
		t.Fatalf("RunContainerBackup() error: %v", err)
	}

	if !res.IsCompose {
		t.Error("expected IsCompose to be true")
	}
	if !res.IsDatabase {
		t.Error("expected IsDatabase to be true")
	}
	if !strings.Contains(res.DatabaseDump, "blog-mysql.sql.gz") {
		t.Errorf("res.DatabaseDump = %q, want to contain blog-mysql.sql.gz", res.DatabaseDump)
	}

	// Verify assetStore saved as database type
	savedAsset, err := assetStore.Get("blog-mysql")
	if err != nil {
		t.Fatalf("assetStore.Get('blog-mysql') failed: %v", err)
	}
	if savedAsset.Type != asset.TypeDatabase {
		t.Errorf("savedAsset.Type = %v, want database", savedAsset.Type)
	}
	if savedAsset.Engine != "mysql" {
		t.Errorf("savedAsset.Engine = %q, want mysql", savedAsset.Engine)
	}
}

// TestRunContainerBackup_ScratchNamespaceIsolatesUserDirs is the regression for
// the P0 data-loss defect: a Compose container's working directory is the
// operator's own project directory, where "dumps/" and "volumes/" are common
// live-data locations. Every artifact and every cleanup target must therefore
// be namespaced under ".opspulse/".
func TestRunContainerBackup_ScratchNamespaceIsolatesUserDirs(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	backupRepo := storage.NewBackupRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

	_ = serverStore.Save(server.Server{Name: "vps-07", Host: "10.0.0.7", User: "root"})
	_ = backupStore.Save(Job{Name: "seed", Server: "vps-07", Paths: []string{"/var/log"}, Backend: "/mnt/repo"})

	const projectDir = "/opt/blog"
	const scratchDir = "/opt/blog/.opspulse"

	// A database container in a Compose project: its physical data volume is
	// replaced by a logical hot dump, and an unrelated named volume must be
	// archived as a tar.
	inspectJSON := `[{
		"Id": "bcd12345",
		"Name": "/blog-db",
		"Config": {
			"Image": "postgres:16",
			"Labels": {
				"com.docker.compose.project": "blog",
				"com.docker.compose.service": "db",
				"com.docker.compose.project.working_dir": "/opt/blog"
			}
		},
		"Mounts": [
			{"Type": "volume", "Name": "blog-db-data", "Source": "/var/lib/docker/volumes/blog-db-data/_data", "Destination": "/var/lib/postgresql/data", "RW": true},
			{"Type": "volume", "Name": "blog-uploads", "Source": "/var/lib/docker/volumes/blog-uploads/_data", "Destination": "/var/lib/app/uploads", "RW": true}
		]
	}]`

	exec := &dynamicMockExecutor{
		inspectJSON:  inspectJSON,
		resticOutput: `{"message_type":"summary","files_new":1,"data_added":1,"total_duration":0.1,"snapshot_id":"snap-scratch"}`,
	}

	runner := NewRunnerWithStores(exec, serverStore, backupRepo, backupStore, assetStore)

	var buf bytes.Buffer
	res, err := runner.RunContainerBackup(context.Background(), ContainerBackupOptions{
		Server:        "vps-07",
		ContainerName: "blog-db",
	}, &buf)
	if err != nil {
		t.Fatalf("RunContainerBackup() error: %v", err)
	}

	// The pre-run cleanup is the exact command that used to destroy live data.
	const wantCleanup = "rm -rf '/opt/blog/.opspulse/dumps' '/opt/blog/.opspulse/volumes'"
	if got := exec.executedScripts["clean-stale-dumps"]; got != wantCleanup {
		t.Errorf("stale cleanup script = %q, want %q", got, wantCleanup)
	}
	for _, forbidden := range []string{"'/opt/blog/dumps'", "'/opt/blog/volumes'"} {
		if strings.Contains(exec.executedScripts["clean-stale-dumps"], forbidden) {
			t.Errorf("stale cleanup must never target the operator's own directory %s: %q",
				forbidden, exec.executedScripts["clean-stale-dumps"])
		}
		// The user's live directories must not be named as cleanup targets anywhere.
		for task, script := range exec.executedScripts {
			if task == "clean-stale-dumps" {
				continue
			}
			if strings.Contains(script, "rm -rf "+forbidden) {
				t.Errorf("task %s removes the operator's own directory %s: %q", task, forbidden, script)
			}
		}
	}

	// Volume archives, dumps and the manifest all land in the scratch namespace.
	exportScript := exec.executedScripts["export-vol-blog-uploads"]
	if !strings.Contains(exportScript, "'/opt/blog/.opspulse/volumes/blog-uploads':/dst") {
		t.Errorf("volume archive directory is not namespaced under %s: %q", scratchDir, exportScript)
	}
	if !strings.Contains(exportScript, "tar cpf /dst/data.tar") {
		t.Errorf("volume export does not write data.tar into the archive directory: %q", exportScript)
	}
	if !strings.Contains(exportScript, "-v 'blog-uploads':/src:ro") {
		t.Errorf("volume export lost the source volume: %q", exportScript)
	}

	dumpScript := exec.executedScripts["dump-blog-db"]
	if !strings.Contains(dumpScript, "/opt/blog/.opspulse/dumps/blog-db.sql.gz") {
		t.Errorf("hot dump is not namespaced under %s: %q", scratchDir, dumpScript)
	}

	manifestScript := exec.executedScripts["write-manifest-blog-db"]
	if !strings.Contains(manifestScript, "/opt/blog/.opspulse/manifest.yaml") {
		t.Errorf("manifest is not written under %s: %q", scratchDir, manifestScript)
	}

	// The restic read root stays the project directory, which now contains the
	// scratch artifacts; nothing was dropped from the backup set.
	hasPath := false
	for _, p := range res.Paths {
		if p == projectDir {
			hasPath = true
		}
	}
	if !hasPath {
		t.Errorf("res.Paths = %v, want it to contain %q", res.Paths, projectDir)
	}
}

// failingTaskExecutor fails every task whose name carries the given prefix, so
// the failure paths that run after RunContainerBackup wrote its 'running' audit
// row but before executeRun finalizes it can be exercised deterministically.
type failingTaskExecutor struct {
	dynamicMockExecutor
	failPrefix string
}

func (m *failingTaskExecutor) Execute(ctx context.Context, target executor.Target, taskName, script string, output io.Writer) (*executor.Result, error) {
	if strings.HasPrefix(taskName, m.failPrefix) {
		return nil, fmt.Errorf("simulated failure of %s", taskName)
	}
	return m.dynamicMockExecutor.Execute(ctx, target, taskName, script, output)
}

// TestRunContainerBackup_FailedPreparationClosesAuditRow is the regression for
// audit rows that stayed 'running' forever: the row is written before the target
// is touched, so every failure up to executeRun must still close it, otherwise
// `ops backup status` keeps reporting RUNNING and the real failure stays hidden.
func TestRunContainerBackup_FailedPreparationClosesAuditRow(t *testing.T) {
	inspectJSON := `[{
		"Id": "abc12345",
		"Name": "/nginx-test",
		"Config": {"Image": "nginx:alpine"},
		"Mounts": [
			{"Type": "volume", "Name": "nginx-data", "Source": "/var/lib/docker/volumes/nginx-data/_data", "Destination": "/var/lib/data", "RW": true}
		]
	}]`

	tests := []struct {
		name       string
		failPrefix string
	}{
		{"inspect fails", "inspect-"},
		{"volume export fails", "export-vol-"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
			if err != nil {
				t.Fatalf("storage.Open() error: %v", err)
			}
			defer func() { _ = db.Close() }()

			backupRepo := storage.NewBackupRepo(db)
			serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
			backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
			assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

			_ = serverStore.Save(server.Server{Name: "vps-09", Host: "203.0.113.10", User: "root"})
			_ = backupStore.Save(Job{Name: "seed", Server: "vps-09", Paths: []string{"/var/log"}, Backend: "/mnt/repo"})

			exec := &failingTaskExecutor{
				dynamicMockExecutor: dynamicMockExecutor{inspectJSON: inspectJSON},
				failPrefix:          tc.failPrefix,
			}
			runner := NewRunnerWithStores(exec, serverStore, backupRepo, backupStore, assetStore)

			var buf bytes.Buffer
			res, err := runner.RunContainerBackup(context.Background(), ContainerBackupOptions{
				Server:        "vps-09",
				ContainerName: "nginx-test",
			}, &buf)
			if err == nil {
				t.Fatalf("RunContainerBackup() error = nil, want the simulated %s failure", tc.failPrefix)
			}
			if res != nil {
				t.Errorf("RunContainerBackup() result = %+v, want nil on failure", res)
			}

			runs, err := backupRepo.ListRuns(context.Background(), "nginx-test", 10)
			if err != nil {
				t.Fatalf("ListRuns() error: %v", err)
			}
			if len(runs) != 1 {
				t.Fatalf("recorded runs = %d, want exactly 1 (the aborted attempt)", len(runs))
			}

			got := runs[0]
			if got.Status != "failed" {
				t.Errorf("run status = %q, want \"failed\": a 'running' row is reported as RUNNING forever", got.Status)
			}
			if got.FinishedAt == nil {
				t.Error("run FinishedAt = nil, want the abort time")
			}
			if !strings.Contains(got.ErrorMessage, tc.failPrefix) {
				t.Errorf("run ErrorMessage = %q, want it to name the failed step %q", got.ErrorMessage, tc.failPrefix)
			}
		})
	}
}

// TestRunContainerBackup_SensitiveEnvRequiresConfirmation is the regression for
// silently writing credentials into a reverse-generated compose.yaml: the file
// lands on the target host and inside the backup snapshot, so a container whose
// environment holds a password or token must produce a risk report and an
// explicit confirmation before anything is written.
func TestRunContainerBackup_SensitiveEnvRequiresConfirmation(t *testing.T) {
	const inspectJSON = `[{
		"Id": "abc12345",
		"Name": "/nginx-test",
		"Config": {
			"Image": "nginx:alpine",
			"Env": ["MYSQL_ROOT_PASSWORD=supersecret", "API_TOKEN=abc123", "TZ=UTC", "PATH=/usr/local/bin"]
		}
	}]`

	newHarness := func(t *testing.T) (*Runner, *dynamicMockExecutor, *storage.BackupRepo) {
		t.Helper()
		tmpDir := t.TempDir()
		db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
		if err != nil {
			t.Fatalf("storage.Open() error: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })

		backupRepo := storage.NewBackupRepo(db)
		serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
		backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
		assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

		_ = serverStore.Save(server.Server{Name: "vps-10", Host: "203.0.113.10", User: "root"})
		// Seed a job so the backup can inherit a repository backend.
		_ = backupStore.Save(Job{Name: "seed", Server: "vps-10", Paths: []string{"/var/log"}, Backend: "/mnt/repo"})

		exec := &dynamicMockExecutor{
			inspectJSON:  inspectJSON,
			resticOutput: `{"message_type":"summary","files_new":1,"data_added":1,"total_duration":0.1,"snapshot_id":"snap-env"}`,
		}
		return NewRunnerWithStores(exec, serverStore, backupRepo, backupStore, assetStore), exec, backupRepo
	}

	assertAbortedWithoutCompose := func(t *testing.T, exec *dynamicMockExecutor, repo *storage.BackupRepo, out string) {
		t.Helper()
		if _, ok := exec.executedScripts["write-compose-nginx-test"]; ok {
			t.Error("generated compose.yaml was written despite the missing confirmation")
		}
		if !strings.Contains(out, "MYSQL_ROOT_PASSWORD") || !strings.Contains(out, "API_TOKEN") {
			t.Errorf("risk report did not name the sensitive variables:\n%s", out)
		}
		if strings.Contains(out, "supersecret") || strings.Contains(out, "abc123") {
			t.Errorf("risk report leaked a secret value:\n%s", out)
		}

		runs, err := repo.ListRuns(context.Background(), "nginx-test", 10)
		if err != nil {
			t.Fatalf("ListRuns() error: %v", err)
		}
		if len(runs) != 1 {
			t.Fatalf("recorded runs = %d, want exactly 1 (the aborted attempt)", len(runs))
		}
		if runs[0].Status != "failed" {
			t.Errorf("run status = %q, want \"failed\"", runs[0].Status)
		}
	}

	t.Run("no confirmation callback refuses", func(t *testing.T) {
		runner, exec, repo := newHarness(t)

		var buf bytes.Buffer
		res, err := runner.RunContainerBackup(context.Background(), ContainerBackupOptions{
			Server:        "vps-10",
			ContainerName: "nginx-test",
		}, &buf)
		if err == nil {
			t.Fatal("RunContainerBackup() error = nil, want a refusal")
		}
		if res != nil {
			t.Errorf("RunContainerBackup() result = %+v, want nil on refusal", res)
		}
		assertAbortedWithoutCompose(t, exec, repo, buf.String())
	})

	t.Run("declined confirmation aborts", func(t *testing.T) {
		runner, exec, repo := newHarness(t)

		var buf bytes.Buffer
		res, err := runner.RunContainerBackup(context.Background(), ContainerBackupOptions{
			Server:              "vps-10",
			ContainerName:       "nginx-test",
			ConfirmSensitiveEnv: func([]string) error { return fmt.Errorf("declined by operator") },
		}, &buf)
		if err == nil {
			t.Fatal("RunContainerBackup() error = nil, want the declined confirmation to abort")
		}
		if res != nil {
			t.Errorf("RunContainerBackup() result = %+v, want nil on refusal", res)
		}
		assertAbortedWithoutCompose(t, exec, repo, buf.String())
	})

	t.Run("confirmed run writes compose", func(t *testing.T) {
		runner, exec, _ := newHarness(t)

		var gotKeys []string
		var buf bytes.Buffer
		res, err := runner.RunContainerBackup(context.Background(), ContainerBackupOptions{
			Server:        "vps-10",
			ContainerName: "nginx-test",
			ConfirmSensitiveEnv: func(keys []string) error {
				gotKeys = keys
				return nil
			},
		}, &buf)
		if err != nil {
			t.Fatalf("RunContainerBackup() error: %v", err)
		}
		if res == nil || res.SnapshotID != "snap-env" {
			t.Fatalf("RunContainerBackup() result = %+v, want a completed backup", res)
		}
		if strings.Join(gotKeys, ",") != "API_TOKEN,MYSQL_ROOT_PASSWORD" {
			t.Errorf("ConfirmSensitiveEnv keys = %v, want [API_TOKEN MYSQL_ROOT_PASSWORD]", gotKeys)
		}
		if _, ok := exec.executedScripts["write-compose-nginx-test"]; !ok {
			t.Error("generated compose.yaml was not written after confirmation")
		}
	})
}
