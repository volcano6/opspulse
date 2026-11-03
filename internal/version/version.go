// Package version provides version, commit, and build date information.
package version

import "runtime/debug"

// Build-time variables injected via ldflags.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func init() {
	if info, ok := debug.ReadBuildInfo(); ok {
		applyBuildInfo(info)
	}
}

// applyBuildInfo fills commit/date metadata that plain `go build` / `go install`
// does not receive through ldflags. Product versions are intentionally not
// inferred here: the rolling source branch has no version series.
func applyBuildInfo(info *debug.BuildInfo) {
	if info == nil {
		return
	}

	var revision, sourceDate string
	dirty := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.time":
			sourceDate = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}

	if Commit == "none" && revision != "" {
		if len(revision) > 12 {
			revision = revision[:12]
		}
		if dirty {
			revision += "-dirty"
		}
		Commit = revision
	}
	if Date == "unknown" && sourceDate != "" {
		Date = sourceDate
	}
}
