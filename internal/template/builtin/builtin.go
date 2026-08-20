// Package builtin provides embedded built-in script templates.
package builtin

import "embed"

// FS embeds all built-in shell scripts.
//
//go:embed *.sh
var FS embed.FS
