package version

import (
	"runtime/debug"
	"testing"
)

func TestApplyBuildInfoFillsUnsetMetadata(t *testing.T) {
	restore := saveMetadata()
	defer restore()

	Version, Commit, Date = "dev", "none", "unknown"
	applyBuildInfo(&debug.BuildInfo{
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef"},
			{Key: "vcs.time", Value: "2026-11-02T00:00:00Z"},
		},
	})

	if Version != "dev" {
		t.Errorf("Version = %q, want dev", Version)
	}
	if Commit != "0123456789ab" {
		t.Errorf("Commit = %q, want 0123456789ab", Commit)
	}
	if Date != "2026-11-02T00:00:00Z" {
		t.Errorf("Date = %q, want source VCS time", Date)
	}
}

func TestApplyBuildInfoPreservesInjectedMetadata(t *testing.T) {
	restore := saveMetadata()
	defer restore()

	Version, Commit, Date = "build-version", "build-commit", "build-date"
	applyBuildInfo(&debug.BuildInfo{
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef0123456789"},
			{Key: "vcs.time", Value: "2030-01-01T00:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	})

	if Version != "build-version" || Commit != "build-commit" || Date != "build-date" {
		t.Errorf("injected metadata changed: %q %q %q", Version, Commit, Date)
	}
}

func TestApplyBuildInfoMarksDirtySource(t *testing.T) {
	restore := saveMetadata()
	defer restore()

	Version, Commit, Date = "dev", "none", "unknown"
	applyBuildInfo(&debug.BuildInfo{
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "fedcba9876543210"},
			{Key: "vcs.modified", Value: "true"},
		},
	})

	if Version != "dev" {
		t.Errorf("Version = %q, want dev for a devel build", Version)
	}
	if Commit != "fedcba987654-dirty" {
		t.Errorf("Commit = %q, want dirty source revision", Commit)
	}
}

func saveMetadata() func() {
	version, commit, date := Version, Commit, Date
	return func() {
		Version, Commit, Date = version, commit, date
	}
}
