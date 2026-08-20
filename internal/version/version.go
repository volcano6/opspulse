// Package version provides version, commit, and build date information.
package version

// Build-time variables injected via ldflags.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)
