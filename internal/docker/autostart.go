package docker

import (
	"fmt"
	"strings"

	"github.com/volcano6/opspulse/internal/shellquote"
)

// AutoStartOptions defines parameters for auto-starting restored container services.
type AutoStartOptions struct {
	ComposeDirs       []string // Directories containing compose.yaml or docker-compose.yml
	AliasName         string   // Optional project alias name
	DatabaseEngine    string   // "mysql" or "postgres" if database container
	DatabaseContainer string   // Target database container name
	DatabaseDump      string   // Path to .sql.gz dump file
}

// BuildAutoStartScript generates a bash script that adaptively detects 'docker compose' vs 'docker-compose',
// brings up the restored container service(s), waits for database readiness, and imports SQL data.
func BuildAutoStartScript(opts AutoStartOptions) string {
	var sb strings.Builder
	sb.WriteString("#!/usr/bin/env bash\n")
	sb.WriteString("set -euo pipefail\n\n")

	sb.WriteString(`# 1. Adaptively detect Docker Compose command
if docker compose version >/dev/null 2>&1; then
  COMPOSE="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
  COMPOSE="docker-compose"
else
  echo "Error: Neither 'docker compose' nor 'docker-compose' found on target system. Cannot start container services." >&2
  exit 127
fi

echo "Using Compose engine: $COMPOSE"
`)

	if opts.AliasName != "" {
		_, _ = fmt.Fprintf(&sb, "export COMPOSE_PROJECT_NAME=%s\n", shellquote.Quote(opts.AliasName))
	}

	sb.WriteString("\n# 2. Start container services in identified project directories\n")
	for _, dir := range opts.ComposeDirs {
		if dir == "" {
			continue
		}
		// Every interpolated value is single-quoted with shellquote.Quote; Go's
		// %q is a Go escape, not a shell one, and left $(), backticks and !
		// live. The directory is never embedded inside the double-quoted echo
		// string either: command substitution still runs inside double quotes.
		quotedDir := shellquote.Quote(dir)
		_, _ = fmt.Fprintf(&sb, `if [ -d %[1]s ]; then
  for compose_file in "compose.yaml" "compose.yml" "docker-compose.yaml" "docker-compose.yml"; do
    if [ -f %[1]s/"$compose_file" ]; then
      echo "Starting services in " %[1]s " using $compose_file..."
      (cd %[1]s && $COMPOSE -f "$compose_file" up -d)
      break
    fi
  done
fi
`, quotedDir)
	}

	// 3. Database import if applicable
	if opts.DatabaseEngine != "" && opts.DatabaseContainer != "" && opts.DatabaseDump != "" {
		importScript, err := BuildImportScript(opts.DatabaseEngine, opts.DatabaseContainer, opts.DatabaseDump)
		if err == nil && importScript != "" {
			sb.WriteString("\n# 3. Database auto-import hook\n")
			// Strip the shebang and set directive line by line to avoid rigid formatting coupling
			lines := strings.Split(importScript, "\n")
			var bodyLines []string
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "#!") || strings.HasPrefix(trimmed, "set -") {
					continue
				}
				bodyLines = append(bodyLines, line)
			}
			cleanScript := strings.TrimLeft(strings.Join(bodyLines, "\n"), "\n")
			sb.WriteString(cleanScript)
		}
	}

	sb.WriteString("\necho 'Container startup sequence completed successfully.'\n")
	return sb.String()
}
