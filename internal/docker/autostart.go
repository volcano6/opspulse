package docker

import (
	"fmt"
	"strings"
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
  echo "Warning: Neither 'docker compose' nor 'docker-compose' found on target system. Skipping container startup." >&2
  exit 0
fi

echo "Using Compose engine: $COMPOSE"
`)

	if opts.AliasName != "" {
		_, _ = fmt.Fprintf(&sb, "export COMPOSE_PROJECT_NAME=%q\n", opts.AliasName)
	}

	sb.WriteString("\n# 2. Start container services in identified project directories\n")
	for _, dir := range opts.ComposeDirs {
		if dir == "" {
			continue
		}
		_, _ = fmt.Fprintf(&sb, `if [ -d %q ]; then
  for compose_file in "compose.yaml" "compose.yml" "docker-compose.yaml" "docker-compose.yml"; do
    if [ -f %q/"$compose_file" ]; then
      echo "Starting services in %q using $compose_file..."
      (cd %q && $COMPOSE -f "$compose_file" up -d)
      break
    fi
  done
fi
`, dir, dir, dir, dir)
	}

	// 3. Database import if applicable
	if opts.DatabaseEngine != "" && opts.DatabaseContainer != "" && opts.DatabaseDump != "" {
		importScript, err := BuildImportScript(opts.DatabaseEngine, opts.DatabaseContainer, opts.DatabaseDump)
		if err == nil && importScript != "" {
			sb.WriteString("\n# 3. Database auto-import hook\n")
			// Strip the shebang from the sub-script
			cleanScript := strings.TrimPrefix(importScript, "#!/usr/bin/env bash\nset -euo pipefail\n\n")
			sb.WriteString(cleanScript)
		}
	}

	sb.WriteString("\necho 'Container startup sequence completed successfully.'\n")
	return sb.String()
}
