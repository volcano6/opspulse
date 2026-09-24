// Package cliutil holds CLI-facing helpers shared by more than one command, so
// that a rule with a single meaning cannot drift between commands.
package cliutil

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	// DefaultParallel is the concurrency every command falls back to when the
	// user does not ask for a specific one.
	DefaultParallel = 5

	// Unlimited is the resolved value meaning "no concurrency limit". Callers
	// translate it themselves: there is no single number that stands for
	// "as many as there is work", because that depends on the batch size.
	Unlimited = -1
)

// ParseParallelism turns a raw --parallel/-j value into a concurrency limit.
//
// Accepted forms: the empty string and "0" mean DefaultParallel; "unlimited"
// (in any casing) means Unlimited; any positive integer means itself. Anything
// else -- including a negative integer -- is an error, because a typo must not
// silently pick a concurrency the user did not ask for.
//
// changed reports whether the user typed the flag explicitly. Because "0" used
// to mean Unlimited in 'ops backup run', an explicit "0" now earns a
// deprecation notice on warn: the value itself still resolves to
// DefaultParallel, so behaviour is uniform across commands. A nil warn writer
// silences the notice.
func ParseParallelism(spec string, changed bool, warn io.Writer) (int, error) {
	trimmed := strings.TrimSpace(spec)

	switch {
	case trimmed == "":
		return DefaultParallel, nil
	case strings.EqualFold(trimmed, "unlimited"):
		return Unlimited, nil
	}

	n, err := strconv.Atoi(trimmed)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid --parallel value %q: use a positive integer, or \"unlimited\" for no limit (0 means the default of %d)", trimmed, DefaultParallel)
	}

	if n == 0 {
		if changed && warn != nil {
			fmt.Fprintf(warn, "⚠️  --parallel 0 now means the default concurrency (%d); pass --parallel unlimited to keep the previous behavior\n", DefaultParallel)
		}
		return DefaultParallel, nil
	}

	return n, nil
}
