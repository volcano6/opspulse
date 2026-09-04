package docker

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
	dst := strings.TrimSpace(destPath)
	if dst == "" {
		return "", ErrEmptyDumpPath
	}

	dir := filepath.Dir(dst)

	var sb strings.Builder
	sb.WriteString("#!/usr/bin/env bash\n")
	sb.WriteString("set -euo pipefail\n\n")

	// Ensure destination directory exists
	_, _ = fmt.Fprintf(&sb, "mkdir -p %q\n\n", dir)

	switch canonicalEngine {
	case EngineMySQL:
		_, _ = fmt.Fprintf(&sb, `# Dump MySQL/MariaDB database container %s
docker exec %q sh -c '
  if [ -n "${MYSQL_ROOT_PASSWORD:-}" ]; then
    PASS="-p$MYSQL_ROOT_PASSWORD"
  elif [ -n "${MARIADB_ROOT_PASSWORD:-}" ]; then
    PASS="-p$MARIADB_ROOT_PASSWORD"
  else
    PASS=""
  fi
  mysqldump --single-transaction --quick -u root $PASS --all-databases
' | gzip > %q
`, cName, cName, dst)

	case EnginePostgres:
		_, _ = fmt.Fprintf(&sb, `# Dump PostgreSQL database container %s
docker exec %q sh -c '
  export PGPASSWORD="${POSTGRES_PASSWORD:-}"
  pg_dumpall -U "${POSTGRES_USER:-postgres}"
' | gzip > %q
`, cName, cName, dst)
	}

	return sb.String(), nil
}

// BuildImportScript generates a bash script to wait for the target database container to be ready
// and import a compressed SQL dump.
func BuildImportScript(engine, containerName, srcPath string) (string, error) {
	canonicalEngine, err := NormalizeDatabaseEngine(engine)
	if err != nil {
		return "", err
	}
	cName := strings.TrimSpace(containerName)
	if cName == "" {
		return "", ErrEmptyContainerName
	}
	src := strings.TrimSpace(srcPath)
	if src == "" {
		return "", ErrEmptyDumpPath
	}

	var sb strings.Builder
	sb.WriteString("#!/usr/bin/env bash\n")
	sb.WriteString("set -euo pipefail\n\n")

	// Check if dump file exists
	_, _ = fmt.Fprintf(&sb, `if [ ! -f %q ]; then
  echo "Dump file %q not found, skipping database import." >&2
  exit 0
fi

`, src, src)

	switch canonicalEngine {
	case EngineMySQL:
		_, _ = fmt.Fprintf(&sb, `# Wait for MySQL container %s to accept connections
echo "Waiting for MySQL in container %q to become ready..."
ready=0
for i in $(seq 1 60); do
  if docker exec %q sh -c '
    if [ -n "${MYSQL_ROOT_PASSWORD:-}" ]; then
      PASS="-p$MYSQL_ROOT_PASSWORD"
    elif [ -n "${MARIADB_ROOT_PASSWORD:-}" ]; then
      PASS="-p$MARIADB_ROOT_PASSWORD"
    else
      PASS=""
    fi
    mysqladmin ping -u root $PASS --silent
  ' >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done

if [ "$ready" -ne 1 ]; then
  echo "Error: Timed out waiting for MySQL container %q to be ready." >&2
  exit 1
fi

echo "MySQL is ready. Importing database dump from %q..."
gunzip -c %q | docker exec -i %q sh -c '
  if [ -n "${MYSQL_ROOT_PASSWORD:-}" ]; then
    PASS="-p$MYSQL_ROOT_PASSWORD"
  elif [ -n "${MARIADB_ROOT_PASSWORD:-}" ]; then
    PASS="-p$MARIADB_ROOT_PASSWORD"
  else
    PASS=""
  fi
  mysql -u root $PASS
'
echo "Database import into %q completed successfully."
`, cName, cName, cName, cName, src, src, cName, cName)

	case EnginePostgres:
		_, _ = fmt.Fprintf(&sb, `# Wait for PostgreSQL container %s to accept connections
echo "Waiting for PostgreSQL in container %q to become ready..."
ready=0
for i in $(seq 1 60); do
  if docker exec %q sh -c 'pg_isready -U "${POSTGRES_USER:-postgres}"' >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done

if [ "$ready" -ne 1 ]; then
  echo "Error: Timed out waiting for PostgreSQL container %q to be ready." >&2
  exit 1
fi

echo "PostgreSQL is ready. Importing database dump from %q..."
gunzip -c %q | docker exec -i %q sh -c '
  export PGPASSWORD="${POSTGRES_PASSWORD:-}"
  psql -U "${POSTGRES_USER:-postgres}"
'
echo "Database import into %q completed successfully."
`, cName, cName, cName, cName, src, src, cName, cName)
	}

	return sb.String(), nil
}
