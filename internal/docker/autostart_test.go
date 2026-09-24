package docker

import (
	"os"
	"os/exec"
	"path/filepath"
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
	if !strings.Contains(script, `export COMPOSE_PROJECT_NAME='nginx-prod'`) {
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
	if !strings.Contains(script, "gunzip -c '/tmp/opspulse-dumps/blog-db.sql.gz'") {
		t.Error("script missing database dump import pipeline")
	}
}

func TestBuildAutoStartScript_MissingComposeExit127(t *testing.T) {
	opts := AutoStartOptions{
		ComposeDirs: []string{"/var/lib/opspulse/containers/app"},
	}
	script := BuildAutoStartScript(opts)
	if !strings.Contains(script, "exit 127") {
		t.Error("expected autostart script to contain 'exit 127' when neither compose engine is available")
	}
	if !strings.Contains(script, "Neither 'docker compose' nor 'docker-compose' found") {
		t.Error("expected autostart script to output missing compose error message")
	}
}

// TestBuildAutoStartScript_InterpolatedValuesAreLiteral executes the script
// with attacker-shaped alias and directory values. Both reach the target host
// through CLI flags (`--as`, `--target`/job paths) without further validation
// on the restore side, and used to be interpolated with Go's %q, which passes
// $(), backticks and ! through to the shell unescaped.
func TestBuildAutoStartScript_InterpolatedValuesAreLiteral(t *testing.T) {
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	payloads := []struct {
		name  string
		value string
	}{
		{"command substitution", "$(touch pwned)"},
		{"backticks", "`touch pwned`"},
		{"semicolon chain", "a; touch pwned"},
		{"single quote breakout", "a'b"},
		{"double quote breakout", `a"; touch pwned; echo "`},
	}

	for _, tc := range payloads {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()

			// Make the compose directory real so the start branch actually runs.
			dir := filepath.Join(work, tc.value)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q): %v", dir, err)
			}
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
				t.Fatalf("write compose.yaml: %v", err)
			}

			// Stub engines so the script selects a compose command.
			binDir := t.TempDir()
			for _, name := range []string{"docker", "docker-compose"} {
				if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { // #nosec G306 -- test stub
					t.Fatalf("write %s stub: %v", name, err)
				}
			}

			script := BuildAutoStartScript(AutoStartOptions{
				ComposeDirs: []string{dir},
				AliasName:   tc.value,
			})

			cmd := exec.Command(shell, "-c", script)
			cmd.Dir = work
			cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("generated script failed: %v\n%s\nscript:\n%s", err, out, script)
			}

			if _, statErr := os.Stat(filepath.Join(work, "pwned")); statErr == nil {
				t.Errorf("payload %q executed on the target host\nscript:\n%s", tc.value, script)
			}
		})
	}
}
