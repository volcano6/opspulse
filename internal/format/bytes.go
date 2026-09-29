// Package format renders numeric values for human-readable terminal output.
//
// It deliberately depends on the standard library only, so any package may
// import it without risking an import cycle.
package format

import "fmt"

// Bytes converts a byte count into a human-readable string (e.g. 10.5 MB).
//
// Values at or below zero render as "0 B". The unit table stops at EB, and the
// loop is bounded by the table length, so byte counts at or above 1 EiB (2^60)
// render as whole exbibytes instead of indexing past the end of the table.
func Bytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB", "EB"}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < len(units)-1; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %s", float64(b)/float64(div), units[exp])
}
