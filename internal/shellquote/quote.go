// Package shellquote provides robust POSIX shell escaping and environment variable validation.
package shellquote

import (
	"regexp"
	"strings"
)

var envNameRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidEnvName checks if an environment variable name matches standard POSIX naming: ^[A-Za-z_][A-Za-z0-9_]*$
func ValidEnvName(name string) bool {
	return envNameRegex.MatchString(name)
}

// Quote returns a POSIX-compliant single-quoted string.
// Single quotes in POSIX shell preserve the literal value of all characters within the quotes,
// preventing any variable expansion ($VAR, $$, $()), command execution (`cmd`),
// or shell metacharacter interpretation.
// Single quote characters (') are escaped as: '\”
func Quote(val string) string {
	if val == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(val, "'", "'\\''") + "'"
}
