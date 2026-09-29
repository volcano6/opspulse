package docker

import (
	"errors"
	"testing"
)

// TestBuildScriptGolden locks the byte-level output of the four script
// builders. These scripts run as bash on remote targets; any behavioural
// change (quoting, image fallback, engine payload) must be intentional.
func TestBuildScriptGolden(t *testing.T) {
	tests := []struct {
		name  string
		build func() (string, error)
		want  string
	}{
		{
			name: "volume-export",
			build: func() (string, error) {
				return BuildVolumeExportScript("my-vol", "/var/lib/opspulse/containers/app/volumes/my-vol/data.tar"), nil
			},
			want: "mkdir -p '/var/lib/opspulse/containers/app/volumes/my-vol'\n" +
				"HELPER_IMG=\"${OPSPULSE_HELPER_IMAGE:-alpine:3.20}\"\n" +
				"if ! docker image inspect \"$HELPER_IMG\" >/dev/null 2>&1; then\n" +
				"  if docker image inspect busybox:1.36 >/dev/null 2>&1; then\n" +
				"    HELPER_IMG=\"busybox:1.36\"\n" +
				"  elif docker image inspect alpine >/dev/null 2>&1; then\n" +
				"    HELPER_IMG=\"alpine\"\n" +
				"  elif docker image inspect busybox >/dev/null 2>&1; then\n" +
				"    HELPER_IMG=\"busybox\"\n" +
				"  fi\n" +
				"fi\n" +
				"docker run --rm -v 'my-vol':/src:ro -v '/var/lib/opspulse/containers/app/volumes/my-vol':/dst \"$HELPER_IMG\" sh -c 'cd /src && tar cpf /dst/data.tar .'",
		},
		{
			name: "volume-export-postgres-like",
			build: func() (string, error) {
				return BuildVolumeExportScript("pg-vol", "/srv/projects/acme/.opspulse/volumes/pg-vol/data.tar"), nil
			},
			want: "mkdir -p '/srv/projects/acme/.opspulse/volumes/pg-vol'\n" +
				"HELPER_IMG=\"${OPSPULSE_HELPER_IMAGE:-alpine:3.20}\"\n" +
				"if ! docker image inspect \"$HELPER_IMG\" >/dev/null 2>&1; then\n" +
				"  if docker image inspect busybox:1.36 >/dev/null 2>&1; then\n" +
				"    HELPER_IMG=\"busybox:1.36\"\n" +
				"  elif docker image inspect alpine >/dev/null 2>&1; then\n" +
				"    HELPER_IMG=\"alpine\"\n" +
				"  elif docker image inspect busybox >/dev/null 2>&1; then\n" +
				"    HELPER_IMG=\"busybox\"\n" +
				"  fi\n" +
				"fi\n" +
				"docker run --rm -v 'pg-vol':/src:ro -v '/srv/projects/acme/.opspulse/volumes/pg-vol':/dst \"$HELPER_IMG\" sh -c 'cd /src && tar cpf /dst/data.tar .'",
		},
		{
			name: "volume-import",
			build: func() (string, error) {
				return BuildVolumeImportScript("my-vol", "/var/lib/opspulse/containers/app/volumes/my-vol/data.tar"), nil
			},
			want: "HELPER_IMG=\"${OPSPULSE_HELPER_IMAGE:-alpine:3.20}\"\n" +
				"if ! docker image inspect \"$HELPER_IMG\" >/dev/null 2>&1; then\n" +
				"  if docker image inspect busybox:1.36 >/dev/null 2>&1; then\n" +
				"    HELPER_IMG=\"busybox:1.36\"\n" +
				"  elif docker image inspect alpine >/dev/null 2>&1; then\n" +
				"    HELPER_IMG=\"alpine\"\n" +
				"  elif docker image inspect busybox >/dev/null 2>&1; then\n" +
				"    HELPER_IMG=\"busybox\"\n" +
				"  fi\n" +
				"fi\n" +
				"docker volume create 'my-vol' >/dev/null\n" +
				"docker run --rm -v 'my-vol':/dst -v '/var/lib/opspulse/containers/app/volumes/my-vol':/src:ro \"$HELPER_IMG\" sh -c 'cd /dst && tar xpf /src/data.tar'",
		},
		{
			name: "volume-import-postgres-like",
			build: func() (string, error) {
				return BuildVolumeImportScript("pg-vol", "/srv/projects/acme/.opspulse/volumes/pg-vol/data.tar"), nil
			},
			want: "HELPER_IMG=\"${OPSPULSE_HELPER_IMAGE:-alpine:3.20}\"\n" +
				"if ! docker image inspect \"$HELPER_IMG\" >/dev/null 2>&1; then\n" +
				"  if docker image inspect busybox:1.36 >/dev/null 2>&1; then\n" +
				"    HELPER_IMG=\"busybox:1.36\"\n" +
				"  elif docker image inspect alpine >/dev/null 2>&1; then\n" +
				"    HELPER_IMG=\"alpine\"\n" +
				"  elif docker image inspect busybox >/dev/null 2>&1; then\n" +
				"    HELPER_IMG=\"busybox\"\n" +
				"  fi\n" +
				"fi\n" +
				"docker volume create 'pg-vol' >/dev/null\n" +
				"docker run --rm -v 'pg-vol':/dst -v '/srv/projects/acme/.opspulse/volumes/pg-vol':/src:ro \"$HELPER_IMG\" sh -c 'cd /dst && tar xpf /src/data.tar'",
		},
		{
			name:  "dump-mysql",
			build: func() (string, error) { return BuildDumpScript("mysql", "db-container", "/tmp/dumps/db.sql.gz") },
			want: "#!/usr/bin/env bash\n" +
				"set -euo pipefail\n" +
				"\n" +
				"mkdir -p '/tmp/dumps'\n" +
				"\n" +
				"# Dump MySQL/MariaDB database container db-container\n" +
				"docker exec 'db-container' sh -c '\n" +
				"  user=\"${MYSQL_USER:-root}\"\n" +
				"  export MYSQL_PWD=\"${MYSQL_ROOT_PASSWORD:-${MARIADB_ROOT_PASSWORD:-${MYSQL_PASSWORD:-}}}\"\n" +
				"  mysqldump --single-transaction --quick -u \"$user\" --all-databases\n" +
				"' | gzip > '/tmp/dumps/db.sql.gz'\n" +
				"chmod 0600 '/tmp/dumps/db.sql.gz'\n",
		},
		{
			name:  "dump-postgres",
			build: func() (string, error) { return BuildDumpScript("postgresql", "pg-container", "/tmp/dumps/pg.sql.gz") },
			want: "#!/usr/bin/env bash\n" +
				"set -euo pipefail\n" +
				"\n" +
				"mkdir -p '/tmp/dumps'\n" +
				"\n" +
				"# Dump PostgreSQL database container pg-container\n" +
				"docker exec 'pg-container' sh -c '\n" +
				"  export PGPASSWORD=\"${POSTGRES_PASSWORD:-}\"\n" +
				"  pg_dumpall -U \"${POSTGRES_USER:-postgres}\"\n" +
				"' | gzip > '/tmp/dumps/pg.sql.gz'\n" +
				"chmod 0600 '/tmp/dumps/pg.sql.gz'\n",
		},
		{
			name:  "import-mysql",
			build: func() (string, error) { return BuildImportScript("mysql", "mysql-srv", "/tmp/dumps/blog.sql.gz") },
			want: "#!/usr/bin/env bash\n" +
				"set -euo pipefail\n" +
				"\n" +
				"if [ ! -f '/tmp/dumps/blog.sql.gz' ]; then\n" +
				"  echo \"Error: Database dump file '/tmp/dumps/blog.sql.gz' not found on target system.\" >&2\n" +
				"  exit 1\n" +
				"fi\n" +
				"\n" +
				"# Wait for MySQL container mysql-srv to accept connections\n" +
				"echo \"Waiting for MySQL in container \" 'mysql-srv' \" to become ready...\"\n" +
				"ready=0\n" +
				"for i in $(seq 1 60); do\n" +
				"  if docker exec 'mysql-srv' sh -c '\n" +
				"    user=\"${MYSQL_USER:-root}\"\n" +
				"    export MYSQL_PWD=\"${MYSQL_ROOT_PASSWORD:-${MARIADB_ROOT_PASSWORD:-${MYSQL_PASSWORD:-}}}\"\n" +
				"    mysqladmin ping -u \"$user\" --silent\n" +
				"  ' >/dev/null 2>&1; then\n" +
				"    ready=1\n" +
				"    break\n" +
				"  fi\n" +
				"  sleep 2\n" +
				"done\n" +
				"\n" +
				"if [ \"$ready\" -ne 1 ]; then\n" +
				"  echo \"Error: Timed out waiting for MySQL container \" 'mysql-srv' \" to be ready.\" >&2\n" +
				"  exit 1\n" +
				"fi\n" +
				"\n" +
				"echo \"MySQL is ready. Importing database dump from \" '/tmp/dumps/blog.sql.gz' \"...\"\n" +
				"gunzip -c '/tmp/dumps/blog.sql.gz' | docker exec -i 'mysql-srv' sh -c '\n" +
				"  user=\"${MYSQL_USER:-root}\"\n" +
				"  export MYSQL_PWD=\"${MYSQL_ROOT_PASSWORD:-${MARIADB_ROOT_PASSWORD:-${MYSQL_PASSWORD:-}}}\"\n" +
				"  mysql -u \"$user\"\n" +
				"'\n" +
				"echo \"Database import into \" 'mysql-srv' \" completed successfully.\"\n",
		},
		{
			name:  "import-postgres",
			build: func() (string, error) { return BuildImportScript("postgres", "pg-srv", "/tmp/dumps/pg.sql.gz") },
			want: "#!/usr/bin/env bash\n" +
				"set -euo pipefail\n" +
				"\n" +
				"if [ ! -f '/tmp/dumps/pg.sql.gz' ]; then\n" +
				"  echo \"Error: Database dump file '/tmp/dumps/pg.sql.gz' not found on target system.\" >&2\n" +
				"  exit 1\n" +
				"fi\n" +
				"\n" +
				"# Wait for PostgreSQL container pg-srv to accept connections\n" +
				"echo \"Waiting for PostgreSQL in container \" 'pg-srv' \" to become ready...\"\n" +
				"ready=0\n" +
				"for i in $(seq 1 60); do\n" +
				"  if docker exec 'pg-srv' sh -c 'pg_isready -U \"${POSTGRES_USER:-postgres}\"' >/dev/null 2>&1; then\n" +
				"    ready=1\n" +
				"    break\n" +
				"  fi\n" +
				"  sleep 2\n" +
				"done\n" +
				"\n" +
				"if [ \"$ready\" -ne 1 ]; then\n" +
				"  echo \"Error: Timed out waiting for PostgreSQL container \" 'pg-srv' \" to be ready.\" >&2\n" +
				"  exit 1\n" +
				"fi\n" +
				"\n" +
				"echo \"PostgreSQL is ready. Importing database dump from \" '/tmp/dumps/pg.sql.gz' \"...\"\n" +
				"gunzip -c '/tmp/dumps/pg.sql.gz' | docker exec -i 'pg-srv' sh -c '\n" +
				"  export PGPASSWORD=\"${POSTGRES_PASSWORD:-}\"\n" +
				"  psql -U \"${POSTGRES_USER:-postgres}\"\n" +
				"'\n" +
				"echo \"Database import into \" 'pg-srv' \" completed successfully.\"\n",
		},
	}

	for _, tc := range tests {
		got, err := tc.build()
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: script mismatch\n--- got ---\n%q\n--- want ---\n%q", tc.name, got, tc.want)
		}
	}
}

// TestValidateScriptInputs asserts the shared validation helper preserves the
// exact error values of the inline checks it replaced.
func TestValidateScriptInputs(t *testing.T) {
	if _, _, _, err := validateScriptInputs("unknown", "c", "/tmp/d.sql.gz"); !errors.Is(err, ErrUnsupportedDatabaseEngine) {
		t.Errorf("unsupported engine error = %v", err)
	}
	if _, _, _, err := validateScriptInputs("mysql", "", "/tmp/d.sql.gz"); !errors.Is(err, ErrEmptyContainerName) {
		t.Errorf("empty container error = %v", err)
	}
	if _, _, _, err := validateScriptInputs("mysql", "c", ""); !errors.Is(err, ErrEmptyDumpPath) {
		t.Errorf("empty path error = %v", err)
	}
	if _, _, _, err := validateScriptInputs("mysql", "a\nb", "/tmp/d.sql.gz"); err == nil || err.Error() != "invalid container name: cannot contain newlines" {
		t.Errorf("newline container error = %v", err)
	}
}
