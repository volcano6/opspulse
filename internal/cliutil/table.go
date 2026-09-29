package cliutil

import (
	"io"
	"text/tabwriter"
)

// NewTabWriter returns the single, shared tabwriter configuration used by every
// `ops` command that renders a column-aligned table. Centralising the five
// literal arguments (minwidth, tabwidth, padding, padchar, flags) means a rule
// with one meaning cannot drift between commands.
func NewTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
}
