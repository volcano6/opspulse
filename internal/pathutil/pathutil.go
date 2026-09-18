// Package pathutil provides small helpers for comparing filesystem paths.
package pathutil

import "strings"

// HasPathPrefix reports whether p is prefix itself or lies beneath it.
//
// Matching is performed on path boundaries rather than on raw byte prefixes,
// so "/development/data" is not treated as being under "/dev", and
// "/system/backups" is not treated as being under "/sys".
//
// Both arguments are expected to be slash-separated paths. A trailing
// separator on prefix is ignored, and an empty prefix never matches.
func HasPathPrefix(p, prefix string) bool {
	if prefix == "" {
		return false
	}
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		// prefix consisted solely of separators, i.e. the filesystem root.
		return strings.HasPrefix(p, "/")
	}
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}
