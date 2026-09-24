package main

import (
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/backup"
)

// Removing an asset that backup jobs still list is allowed — those jobs only
// break at their next run — but it must say so instead of staying silent.
func TestAssetRemoveWarnsAboutReferencingBackupJobs(t *testing.T) {
	root := t.TempDir()
	setTestHome(t, root)

	if err := asset.NewDefaultStore().Save(asset.Asset{ID: "blogdb", Type: asset.TypeDatabase, Source: "/srv/mysql", Engine: "mysql"}); err != nil {
		t.Fatal(err)
	}
	if err := backup.NewDefaultStore().Save(backup.Job{Name: "webnightly", Server: "web", Assets: []string{"blogdb"}, Backend: "local:/srv/restic"}); err != nil {
		t.Fatal(err)
	}

	out := captureStderr(t, func() {
		if err := assetRemoveCmd.RunE(assetRemoveCmd, []string{"blogdb"}); err != nil {
			t.Errorf("a referencing backup job must not block asset removal: %v", err)
		}
	})
	if !strings.Contains(out, "webnightly") {
		t.Errorf("stderr %q does not name the referencing backup job", out)
	}
	if _, err := asset.NewDefaultStore().Get("blogdb"); err == nil {
		t.Error("asset entry should be gone")
	}
}

// A config that cannot be parsed is reported, never treated as "no references".
func TestAssetRemoveReportsUnreadableBackupConfig(t *testing.T) {
	root := t.TempDir()
	setTestHome(t, root)

	if err := asset.NewDefaultStore().Save(asset.Asset{ID: "blogdb", Type: asset.TypeVolume, Source: "blogdata"}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, backup.NewDefaultStore().FilePath())

	out := captureStderr(t, func() {
		if err := assetRemoveCmd.RunE(assetRemoveCmd, []string{"blogdb"}); err != nil {
			t.Errorf("unreadable backup config must not block asset removal: %v", err)
		}
	})
	if !strings.Contains(out, "could not read") {
		t.Errorf("stderr %q does not report the skipped reference check", out)
	}
	if _, err := asset.NewDefaultStore().Get("blogdb"); err == nil {
		t.Error("asset entry should be gone")
	}
}
