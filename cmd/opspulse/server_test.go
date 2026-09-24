package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/server"
)

func TestFormatKeyDisplay(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/home/user"
	}

	tests := []struct {
		input string
		want  string
	}{
		{"~/.ssh/id_rsa", "~/.ssh/id_rsa"},
		{filepath.Join(home, ".ssh", "id_ed25519"), "~/.ssh/id_ed25519"},
		{"/opt/keys/custom.pem", "custom.pem"},
	}

	for _, tt := range tests {
		got := formatKeyDisplay(tt.input)
		if got != tt.want {
			t.Errorf("formatKeyDisplay(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatTagsAndLabels(t *testing.T) {
	s1 := server.Server{Tags: []string{"web", "prod"}}
	if got := formatTagsAndLabels(s1); got != "web,prod" {
		t.Errorf("expected 'web,prod', got %q", got)
	}

	s2 := server.Server{Labels: map[string]string{"env": "staging"}}
	if got := formatTagsAndLabels(s2); got != "env=staging" {
		t.Errorf("expected 'env=staging', got %q", got)
	}

	s3 := server.Server{Tags: []string{"api"}, Labels: map[string]string{"env": "prod"}}
	if got := formatTagsAndLabels(s3); got != "api env=prod" {
		t.Errorf("expected 'api env=prod', got %q", got)
	}

	s4 := server.Server{}
	if got := formatTagsAndLabels(s4); got != "-" {
		t.Errorf("expected '-', got %q", got)
	}

	s5 := server.Server{SkipBatch: true}
	if got := formatTagsAndLabels(s5); got != "[skip-batch]" {
		t.Errorf("expected '[skip-batch]', got %q", got)
	}

	s6 := server.Server{Tags: []string{"prod"}, SkipBatch: true}
	if got := formatTagsAndLabels(s6); got != "prod [skip-batch]" {
		t.Errorf("expected 'prod [skip-batch]', got %q", got)
	}
}

func TestRenderServerTable(t *testing.T) {
	servers := []server.Server{
		{
			Name: "bastion-1",
			Host: "192.0.2.10",
			Port: 22,
			User: "root",
		},
		{
			Name:     "worker-1",
			Host:     "worker-1.example.com",
			Port:     22,
			User:     "root",
			JumpHost: "bastion-1",
		},
		{
			Name:        "db-1",
			Host:        "198.51.100.20",
			Port:        22,
			User:        "ubuntu",
			KeyPath:     "~/.ssh/id_rsa",
			Description: "Tencent VPS",
			Tags:        []string{"prod"},
		},
	}

	var buf bytes.Buffer
	if err := renderServerTable(&buf, servers); err != nil {
		t.Fatalf("renderServerTable error: %v", err)
	}

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 { // Header + 3 servers
		t.Fatalf("expected 4 lines, got %d:\n%s", len(lines), out)
	}

	header := lines[0]
	for _, col := range []string{"NAME", "TARGET", "VIA JUMP", "AUTH", "TAGS", "DESCRIPTION"} {
		if !strings.Contains(header, col) {
			t.Errorf("header missing column %q: %s", col, header)
		}
	}

	if !strings.Contains(lines[1], "root@192.0.2.10") {
		t.Errorf("line 1 missing target root@192.0.2.10: %s", lines[1])
	}
	if !strings.Contains(lines[2], "root@worker-1.example.com") || !strings.Contains(lines[2], "bastion-1") {
		t.Errorf("line 2 missing target or jump host: %s", lines[2])
	}
	if !strings.Contains(lines[3], "ubuntu@198.51.100.20") || !strings.Contains(lines[3], "Tencent VPS") {
		t.Errorf("line 3 missing target or description: %s", lines[3])
	}
}

func TestTopLevelShortcuts(t *testing.T) {
	if testCmd == nil || testCmd.Use != "test <name>" {
		t.Errorf("testCmd not configured properly")
	}
	if infoCmd == nil || infoCmd.Use != "info <name>" {
		t.Errorf("infoCmd not configured properly")
	}
	if testCmd.ValidArgsFunction == nil {
		t.Errorf("testCmd missing server name auto-completion")
	}
	if infoCmd.ValidArgsFunction == nil {
		t.Errorf("infoCmd missing server name auto-completion")
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// blockStoreWrites makes every store write fail deterministically. The stores
// take their cross-process lock on <file>.lock, and a directory cannot be
// opened read-write - not even by root, which makes this reliable in CI.
func blockStoreWrites(t *testing.T, filePath string) {
	t.Helper()
	lockPath := filePath + ".lock"
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove lock file %s: %v", lockPath, err)
	}
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatalf("create lock directory %s: %v", lockPath, err)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	fn()

	os.Stderr = old
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	_ = r.Close()
	return string(data)
}

func TestBackupRefCheckFindsReferencesInJobs(t *testing.T) {
	dir := t.TempDir()
	backupStore := backup.NewStore(filepath.Join(dir, "backups.yaml"))
	for _, job := range []backup.Job{
		{Name: "webnightly", Server: "web", Paths: []string{"/srv/data"}, Backend: "local:/srv/restic"},
		{Name: "webassets", Server: "web", Assets: []string{"blogdb"}, Backend: "local:/srv/restic"},
		{Name: "dbbkup", Server: "db", Paths: []string{"/srv/db"}, Backend: "local:/srv/restic"},
	} {
		if err := backupStore.Save(job); err != nil {
			t.Fatalf("save job %q: %v", job.Name, err)
		}
	}
	assetStore := asset.NewStore(filepath.Join(dir, "assets.yaml"))
	if err := assetStore.Save(asset.Asset{ID: "blogdb", Type: asset.TypeDatabase, Source: "/srv/mysql", Engine: "mysql"}); err != nil {
		t.Fatalf("save asset: %v", err)
	}

	check := loadBackupRefCheck(backupStore, assetStore)
	if len(check.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", check.Warnings)
	}
	if got := strings.Join(check.jobsReferencingServer("web"), ","); got != "webnightly,webassets" {
		t.Errorf("jobsReferencingServer(web) = %q", got)
	}
	if got := strings.Join(check.jobsReferencingAsset("blogdb"), ","); got != "webassets" {
		t.Errorf("jobsReferencingAsset(blogdb) = %q", got)
	}
	if got := check.jobsReferencingServer("absent"); len(got) != 0 {
		t.Errorf("jobsReferencingServer(absent) = %q", got)
	}
	if got := check.jobsReferencingAsset("absent"); len(got) != 0 {
		t.Errorf("jobsReferencingAsset(absent) = %q", got)
	}

	var warned bytes.Buffer
	warnBackupRefCheck(&warned, check, check.jobsReferencingServer("web"), `server "web"`)
	for _, want := range []string{"webnightly", "webassets", `server "web"`} {
		if !strings.Contains(warned.String(), want) {
			t.Errorf("warning %q does not mention %s", warned.String(), want)
		}
	}
	if strings.Contains(warned.String(), "dbbkup") {
		t.Errorf("unrelated job leaked into the warning: %q", warned.String())
	}

	var quiet bytes.Buffer
	warnBackupRefCheck(&quiet, check, nil, `asset "other"`)
	if quiet.Len() != 0 {
		t.Errorf("expected no output for an unreferenced entry, got %q", quiet.String())
	}
}

func TestBackupRefCheckWarnsInsteadOfFailingOnUnreadableConfigs(t *testing.T) {
	dir := t.TempDir()
	backupPath := filepath.Join(dir, "backups.yaml")
	assetPath := filepath.Join(dir, "assets.yaml")
	if err := os.WriteFile(backupPath, []byte("backups: [oops\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assetPath, []byte("assets: [oops\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	check := loadBackupRefCheck(backup.NewStore(backupPath), asset.NewStore(assetPath))
	if len(check.Warnings) != 2 {
		t.Fatalf("expected one warning per unreadable config, got %v", check.Warnings)
	}
	for _, warning := range check.Warnings {
		if !strings.Contains(warning, "could not read") {
			t.Errorf("warning %q does not tell the user the check was skipped", warning)
		}
	}

	missing := loadBackupRefCheck(
		backup.NewStore(filepath.Join(dir, "no-backups.yaml")),
		asset.NewStore(filepath.Join(dir, "no-assets.yaml")),
	)
	if len(missing.Warnings) != 0 {
		t.Errorf("an absent config is not an error, got warnings %v", missing.Warnings)
	}
}

func TestConfirmReferencingJobsOnlyAsksInteractively(t *testing.T) {
	jobs := []string{"webnightly"}

	var out bytes.Buffer
	if err := confirmReferencingJobs(nil, &out, `server "web"`, nil, false, true); err != nil {
		t.Errorf("an entry with no referencing job must not prompt: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("unexpected prompt: %q", out.String())
	}

	// --yes already accepted whatever the removal implies; a script must not be
	// stopped by its own confirmation flag.
	if err := confirmReferencingJobs(nil, &out, `server "web"`, jobs, true, true); err != nil {
		t.Errorf("--yes must not be blocked by a backup job reference: %v", err)
	}
	if err := confirmReferencingJobs(nil, &out, `server "web"`, jobs, false, false); err != nil {
		t.Errorf("a shell that cannot be asked must not be blocked: %v", err)
	}

	var declined bytes.Buffer
	if err := confirmReferencingJobs(strings.NewReader("n\n"), &declined, `server "web"`, jobs, false, true); err == nil {
		t.Fatal("declining the reference confirmation must cancel the removal")
	}
	if !strings.Contains(declined.String(), "webnightly") {
		t.Errorf("prompt %q does not name the referencing job", declined.String())
	}

	if err := confirmReferencingJobs(strings.NewReader("y\n"), io.Discard, `server "web"`, jobs, false, true); err != nil {
		t.Errorf("accepting the reference confirmation must proceed: %v", err)
	}
}

// The inventory entry is written before the managed key is touched: a failed
// write used to leave the entry pointing at a private key that was already
// deleted.
func TestServerRemoveKeepsManagedKeyWhenInventoryWriteFails(t *testing.T) {
	root := t.TempDir()
	setTestHome(t, root)

	keyPath := filepath.Join(root, ".ssh", "opspulse_web")
	writeTestFile(t, keyPath)
	store := server.NewDefaultStore()
	if err := store.Save(server.Server{Name: "web", Host: "10.0.0.1", KeyPath: keyPath}); err != nil {
		t.Fatal(err)
	}
	blockStoreWrites(t, store.FilePath())

	removeYes = true
	t.Cleanup(func() { removeYes = false })

	if err := serverRemoveCmd.RunE(serverRemoveCmd, []string{"web"}); err == nil {
		t.Fatal("expected the removal to fail while the inventory cannot be written")
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("managed key was deleted even though the inventory write failed: %v", err)
	}
	if _, err := server.NewDefaultStore().Get("web"); err != nil {
		t.Errorf("inventory entry should still be present: %v", err)
	}
}

func TestServerRemoveProceedsWithReferencingBackupJobs(t *testing.T) {
	root := t.TempDir()
	setTestHome(t, root)

	store := server.NewDefaultStore()
	if err := store.Save(server.Server{Name: "web", Host: "10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	if err := backup.NewDefaultStore().Save(backup.Job{Name: "webnightly", Server: "web", Paths: []string{"/srv/data"}, Backend: "local:/srv/restic"}); err != nil {
		t.Fatal(err)
	}

	removeYes = true
	t.Cleanup(func() { removeYes = false })

	out := captureStderr(t, func() {
		if err := serverRemoveCmd.RunE(serverRemoveCmd, []string{"web"}); err != nil {
			t.Errorf("--yes must not be blocked by a backup job reference: %v", err)
		}
	})
	if !strings.Contains(out, "webnightly") {
		t.Errorf("stderr %q does not name the referencing backup job", out)
	}
	if _, err := store.Get("web"); err == nil {
		t.Error("server entry should be gone")
	}
}

func TestServerRemoveDeletesManagedKeyAfterSuccessfulRemoval(t *testing.T) {
	root := t.TempDir()
	setTestHome(t, root)

	keyPath := filepath.Join(root, ".ssh", "opspulse_web")
	writeTestFile(t, keyPath)
	writeTestFile(t, keyPath+".pub")
	store := server.NewDefaultStore()
	if err := store.Save(server.Server{Name: "web", Host: "10.0.0.1", KeyPath: keyPath}); err != nil {
		t.Fatal(err)
	}

	removeYes = true
	t.Cleanup(func() { removeYes = false })

	if err := serverRemoveCmd.RunE(serverRemoveCmd, []string{"web"}); err != nil {
		t.Fatalf("serverRemoveCmd failed: %v", err)
	}
	for _, path := range []string{keyPath, keyPath + ".pub"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("managed key %s should have been deleted", path)
		}
	}
}
