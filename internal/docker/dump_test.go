package docker

import (
	"strings"
	"testing"
)

func TestNormalizeDatabaseEngine(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"mysql", EngineMySQL, false},
		{"MySQL", EngineMySQL, false},
		{"mariadb", EngineMySQL, false},
		{"MariaDB", EngineMySQL, false},
		{"postgres", EnginePostgres, false},
		{"PostgreSQL", EnginePostgres, false},
		{"POSTGRES", EnginePostgres, false},
		{"sqlite", "", true},
		{"redis", "", true},
		{"", "", true},
	}

	for _, tc := range tests {
		got, err := NormalizeDatabaseEngine(tc.input)
		if (err != nil) != tc.wantErr {
			t.Errorf("NormalizeDatabaseEngine(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeDatabaseEngine(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestDumpFileName(t *testing.T) {
	if got := DumpFileName("blog-db"); got != "blog-db.sql.gz" {
		t.Errorf("DumpFileName('blog-db') = %q, want blog-db.sql.gz", got)
	}
	if got := DumpFileName(""); got != "database.sql.gz" {
		t.Errorf("DumpFileName('') = %q, want database.sql.gz", got)
	}
}

func TestBuildDumpScript_MySQL(t *testing.T) {
	script, err := BuildDumpScript("mysql", "db-container", "/tmp/dumps/db.sql.gz")
	if err != nil {
		t.Fatalf("BuildDumpScript(mysql) error: %v", err)
	}

	if !strings.Contains(script, "set -euo pipefail") {
		t.Error("script missing set -euo pipefail")
	}
	if !strings.Contains(script, "mysqldump --single-transaction --quick") {
		t.Error("script missing mysqldump command")
	}
	if !strings.Contains(script, "docker exec \"db-container\"") {
		t.Error("script missing docker exec call")
	}
	if !strings.Contains(script, "gzip > \"/tmp/dumps/db.sql.gz\"") {
		t.Error("script missing gzip output redirection to destination")
	}
	if !strings.Contains(script, "mkdir -p \"/tmp/dumps\"") {
		t.Error("script missing destination dir creation")
	}
}

func TestBuildDumpScript_Postgres(t *testing.T) {
	script, err := BuildDumpScript("postgresql", "pg-container", "/tmp/dumps/pg.sql.gz")
	if err != nil {
		t.Fatalf("BuildDumpScript(postgresql) error: %v", err)
	}

	if !strings.Contains(script, "pg_dumpall") {
		t.Error("script missing pg_dumpall command")
	}
	if !strings.Contains(script, "docker exec \"pg-container\"") {
		t.Error("script missing docker exec call")
	}
	if !strings.Contains(script, "gzip > \"/tmp/dumps/pg.sql.gz\"") {
		t.Error("script missing gzip redirection")
	}
}

func TestBuildDumpScript_Errors(t *testing.T) {
	if _, err := BuildDumpScript("unknown", "c", "/tmp/d.sql.gz"); err == nil {
		t.Error("expected error on unsupported engine, got nil")
	}
	if _, err := BuildDumpScript("mysql", "", "/tmp/d.sql.gz"); err == nil {
		t.Error("expected error on empty container name, got nil")
	}
	if _, err := BuildDumpScript("mysql", "c", ""); err == nil {
		t.Error("expected error on empty destPath, got nil")
	}
}

func TestBuildImportScript_MySQL(t *testing.T) {
	script, err := BuildImportScript("mysql", "mysql-srv", "/tmp/dumps/blog.sql.gz")
	if err != nil {
		t.Fatalf("BuildImportScript(mysql) error: %v", err)
	}

	if !strings.Contains(script, "mysqladmin ping") {
		t.Error("script missing mysqladmin ping readiness check")
	}
	if !strings.Contains(script, "gunzip -c \"/tmp/dumps/blog.sql.gz\"") {
		t.Error("script missing gunzip invocation")
	}
	if !strings.Contains(script, "docker exec -i \"mysql-srv\"") {
		t.Error("script missing docker exec -i import invocation")
	}
	if !strings.Contains(script, "mysql -u root") {
		t.Error("script missing mysql command")
	}
}

func TestBuildImportScript_Postgres(t *testing.T) {
	script, err := BuildImportScript("postgres", "pg-srv", "/tmp/dumps/pg.sql.gz")
	if err != nil {
		t.Fatalf("BuildImportScript(postgres) error: %v", err)
	}

	if !strings.Contains(script, "pg_isready") {
		t.Error("script missing pg_isready readiness check")
	}
	if !strings.Contains(script, "gunzip -c \"/tmp/dumps/pg.sql.gz\"") {
		t.Error("script missing gunzip invocation")
	}
	if !strings.Contains(script, "docker exec -i \"pg-srv\"") {
		t.Error("script missing docker exec -i import invocation")
	}
	if !strings.Contains(script, "psql -U") {
		t.Error("script missing psql command")
	}
}

func TestBuildImportScript_Errors(t *testing.T) {
	if _, err := BuildImportScript("unknown", "c", "/tmp/s.sql.gz"); err == nil {
		t.Error("expected error on unsupported engine, got nil")
	}
	if _, err := BuildImportScript("mysql", "", "/tmp/s.sql.gz"); err == nil {
		t.Error("expected error on empty container name, got nil")
	}
	if _, err := BuildImportScript("mysql", "c", ""); err == nil {
		t.Error("expected error on empty srcPath, got nil")
	}
}
