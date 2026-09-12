package docker

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/volcano6/opspulse/internal/shellquote"
)

var (
	// ErrUnsupportedDatabaseEngine is returned when an unsupported database engine is specified.
	ErrUnsupportedDatabaseEngine = errors.New("unsupported database engine (supported: 'mysql', 'postgres')")
	// ErrEmptyContainerName is returned when a container name is empty.
	ErrEmptyContainerName = errors.New("container name cannot be empty")
	// ErrEmptyDumpPath is returned when the dump file path is empty.
	ErrEmptyDumpPath = errors.New("dump path cannot be empty")
)

// Supported database engine identifiers.
const (
	EngineMySQL    = "mysql"
	EnginePostgres = "postgres"
)

// NormalizeDatabaseEngine returns the canonical database engine identifier ("mysql" or "postgres").
func NormalizeDatabaseEngine(engine string) (string, error) {
	norm := strings.TrimSpace(strings.ToLower(engine))
	switch norm {
	case "mysql", "mariadb":
		return EngineMySQL, nil
	case "postgres", "postgresql":
		return EnginePostgres, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedDatabaseEngine, engine)
	}
}

// DumpFileName returns the canonical compressed dump file name for an asset or container.
func DumpFileName(name string) string {
	clean := strings.TrimSpace(name)
	if clean == "" {
		clean = "database"
	}
	return clean + ".sql.gz"
}

// BuildDumpScript generates a bash script to perform an online logical hot dump from a running database container.
// The output is compressed with gzip on the fly to minimize storage and transmission overhead.
func BuildDumpScript(engine, containerName, destPath string) (string, error) {
	canonicalEngine, err := NormalizeDatabaseEngine(engine)
	if err != nil {
		return "", err
	}
	cName := strings.TrimSpace(containerName)
	if cName == "" {
		return "", ErrEmptyContainerName
	}
	if strings.ContainsAny(cName, "\r\n") {
		return "", fmt.Errorf("invalid container name: cannot contain newlines")
	}
	dst := strings.TrimSpace(destPath)
	if dst == "" {
		return "", ErrEmptyDumpPath
	}

	dir := path.Dir(strings.ReplaceAll(dst, "\\", "/"))

	var sb strings.Builder
	sb.WriteString("#!/usr/bin/env bash\n")
	sb.WriteString("set -euo pipefail\n\n")

	// Ensure destination directory exists
	sb.WriteString("mkdir -p " + shellquote.Quote(dir) + "\n\n")

	switch canonicalEngine {
	case EngineMySQL:
		_, _ = fmt.Fprintf(&sb, `# Dump MySQL/MariaDB database container %s
docker exec %s sh -c '
  export MYSQL_PWD="${MYSQL_ROOT_PASSWORD:-${MARIADB_ROOT_PASSWORD:-}}"
  mysqldump --single-transaction --quick -u root --all-databases
' | gzip > %s
chmod 0600 %s
`, cName, shellquote.Quote(cName), shellquote.Quote(dst), shellquote.Quote(dst))

	case EnginePostgres:
		_, _ = fmt.Fprintf(&sb, `# Dump PostgreSQL database container %s
docker exec %s sh -c '
  export PGPASSWORD="${POSTGRES_PASSWORD:-}"
  pg_dumpall -U "${POSTGRES_USER:-postgres}"
' | gzip > %s
chmod 0600 %s
`, cName, shellquote.Quote(cName), shellquote.Quote(dst), shellquote.Quote(dst))
	}

	return sb.String(), nil
}

// BuildImportScript generates a bash script to wait for the target database container to be ready
// and import a compressed SQL dump. If the dump file does not exist, it exits with error 1.
func BuildImportScript(engine, containerName, srcPath string) (string, error) {
	canonicalEngine, err := NormalizeDatabaseEngine(engine)
	if err != nil {
		return "", err
	}
	cName := strings.TrimSpace(containerName)
	if cName == "" {
		return "", ErrEmptyContainerName
	}
	if strings.ContainsAny(cName, "\r\n") {
		return "", fmt.Errorf("invalid container name: cannot contain newlines")
	}
	src := strings.TrimSpace(srcPath)
	if src == "" {
		return "", ErrEmptyDumpPath
	}

	var sb strings.Builder
	sb.WriteString("#!/usr/bin/env bash\n")
	sb.WriteString("set -euo pipefail\n\n")

	// Check if dump file exists; in database restore mode, missing dump is a fatal error
	_, _ = fmt.Fprintf(&sb, `if [ ! -f %s ]; then
  echo "Error: Database dump file %s not found on target system." >&2
  exit 1
fi

`, shellquote.Quote(src), shellquote.Quote(src))

	switch canonicalEngine {
	case EngineMySQL:
		_, _ = fmt.Fprintf(&sb, `# Wait for MySQL container %s to accept connections
echo "Waiting for MySQL in container " %s " to become ready..."
ready=0
for i in $(seq 1 60); do
  if docker exec %s sh -c '
    export MYSQL_PWD="${MYSQL_ROOT_PASSWORD:-${MARIADB_ROOT_PASSWORD:-}}"
    mysqladmin ping -u root --silent
  ' >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done

if [ "$ready" -ne 1 ]; then
  echo "Error: Timed out waiting for MySQL container " %s " to be ready." >&2
  exit 1
fi

echo "MySQL is ready. Importing database dump from " %s "..."
gunzip -c %s | docker exec -i %s sh -c '
  export MYSQL_PWD="${MYSQL_ROOT_PASSWORD:-${MARIADB_ROOT_PASSWORD:-}}"
  mysql -u root
'
echo "Database import into " %s " completed successfully."
`, cName, shellquote.Quote(cName), shellquote.Quote(cName), shellquote.Quote(cName), shellquote.Quote(src), shellquote.Quote(src), shellquote.Quote(cName), shellquote.Quote(cName))

	case EnginePostgres:
		_, _ = fmt.Fprintf(&sb, `# Wait for PostgreSQL container %s to accept connections
echo "Waiting for PostgreSQL in container " %s " to become ready..."
ready=0
for i in $(seq 1 60); do
  if docker exec %s sh -c 'pg_isready -U "${POSTGRES_USER:-postgres}"' >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done

if [ "$ready" -ne 1 ]; then
  echo "Error: Timed out waiting for PostgreSQL container " %s " to be ready." >&2
  exit 1
fi

echo "PostgreSQL is ready. Importing database dump from " %s "..."
gunzip -c %s | docker exec -i %s sh -c '
  export PGPASSWORD="${POSTGRES_PASSWORD:-}"
  psql -U "${POSTGRES_USER:-postgres}"
'
echo "Database import into " %s " completed successfully."
`, cName, shellquote.Quote(cName), shellquote.Quote(cName), shellquote.Quote(cName), shellquote.Quote(src), shellquote.Quote(src), shellquote.Quote(cName), shellquote.Quote(cName))
	}

	return sb.String(), nil
}
