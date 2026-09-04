package docker

import (
	"strings"
	"testing"
)

func TestBuildAutoStartScript_Basic(t *testing.T) {
	opts := AutoStartOptions{
		ComposeDirs: []string{"/var/lib/opspulse/containers/nginx"},
		AliasName:   "nginx-prod",
	}

	script := BuildAutoStartScript(opts)

	if !strings.Contains(script, `COMPOSE="docker compose"`) {
		t.Error("script missing docker compose detection")
	}
	if !strings.Contains(script, `COMPOSE="docker-compose"`) {
		t.Error("script missing docker-compose fallback detection")
	}
	if !strings.Contains(script, `export COMPOSE_PROJECT_NAME="nginx-prod"`) {
		t.Error("script missing COMPOSE_PROJECT_NAME export")
	}
	if !strings.Contains(script, `$COMPOSE -f "$compose_file" up -d`) {
		t.Error("script missing $COMPOSE up -d command")
	}
}

func TestBuildAutoStartScript_WithDatabase(t *testing.T) {
	opts := AutoStartOptions{
		ComposeDirs:       []string{"/opt/blog"},
		DatabaseEngine:    "mysql",
		DatabaseContainer: "blog-db",
		DatabaseDump:      "/tmp/opspulse-dumps/blog-db.sql.gz",
	}

	script := BuildAutoStartScript(opts)

	if !strings.Contains(script, "mysqladmin ping") {
		t.Error("script missing MySQL readiness probe")
	}
	if !strings.Contains(script, "gunzip -c \"/tmp/opspulse-dumps/blog-db.sql.gz\"") {
		t.Error("script missing database dump import pipeline")
	}
}
