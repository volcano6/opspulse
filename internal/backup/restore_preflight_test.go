package backup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/docker"
)

func TestBuildRestorePreflightScriptIsReadOnly(t *testing.T) {
	script, err := BuildRestorePreflightScript(restorePreflightInput{
		ProjectName:        "blog",
		ComposeFile:        "/opt/blog/compose.yaml",
		VolumeArchives:     []string{"/opt/blog/.opspulse/volumes/uploads/data.tar"},
		DatabaseDump:       "/opt/blog/.opspulse/dumps/blog.sql.gz",
		ExpectedContainers: []string{"blog", "blog-db"},
		CrossMachine:       true,
	})
	if err != nil {
		t.Fatalf("BuildRestorePreflightScript() error = %v", err)
	}

	for _, forbidden := range []string{
		"docker volume create",
		"docker run",
		"docker compose up",
		"docker-compose up",
		"docker exec",
		"restic restore",
		"rm -rf",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("preflight script contains mutating command %q:\n%s", forbidden, script)
		}
	}

	for _, want := range []string{
		"docker info",
		"docker compose version",
		"docker image inspect",
		"docker ps -a --format '{{json .}}'",
		"'/opt/blog/compose.yaml'",
		"'/opt/blog/.opspulse/volumes/uploads/data.tar'",
		"'/opt/blog/.opspulse/dumps/blog.sql.gz'",
		restorePreflightEndMarker,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("preflight script is missing %q:\n%s", want, script)
		}
	}
}

func TestBuildRestorePreflightScriptQuotesValues(t *testing.T) {
	script, err := BuildRestorePreflightScript(restorePreflightInput{
		ComposeFile:    "/opt/it's blog/compose.yaml",
		VolumeArchives: []string{"/tmp/a; touch /tmp/pwned.tar"},
	})
	if err != nil {
		t.Fatalf("BuildRestorePreflightScript() error = %v", err)
	}
	if !strings.Contains(script, `'/opt/it'\''s blog/compose.yaml'`) {
		t.Errorf("project path was not shell-quoted:\n%s", script)
	}
	if !strings.Contains(script, `'/tmp/a; touch /tmp/pwned.tar'`) {
		t.Errorf("archive path was not shell-quoted:\n%s", script)
	}
}

func TestParseRestorePreflightOutput(t *testing.T) {
	raw := strings.Join([]string{
		"OPSPULSE_PREFLIGHT_COMPOSE|plugin",
		"OPSPULSE_PREFLIGHT_HELPER|present",
		`{"ID":"1","Names":"blog-web","Image":"nginx","Labels":"com.docker.compose.project=blog,com.docker.compose.service=web"}`,
		"OPSPULSE_PREFLIGHT_ERROR|path_missing|database dump is missing",
		restorePreflightEndMarker,
	}, "\n")

	report, err := ParseRestorePreflightOutput(raw)
	if err != nil {
		t.Fatalf("ParseRestorePreflightOutput() error = %v", err)
	}
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "database dump") {
		t.Errorf("errors = %v, want the dump failure", report.Errors)
	}
	if len(report.Containers) != 1 || report.Containers[0].ComposeProject() != "blog" {
		t.Errorf("containers = %+v, want the blog Compose container", report.Containers)
	}
}

func TestParseRestorePreflightOutputRequiresEndMarker(t *testing.T) {
	if _, err := ParseRestorePreflightOutput("OPSPULSE_PREFLIGHT_COMPOSE|plugin\n"); err == nil {
		t.Fatal("a truncated preflight must not be treated as success")
	}
}

func TestDetectRestoreTargetConflicts(t *testing.T) {
	containers := []docker.ContainerSummary{
		{Name: "blog-web", Labels: map[string]string{docker.LabelComposeProject: "blog"}},
		{Name: "unrelated"},
	}
	conflicts := detectRestoreTargetConflicts(containers, "blog", []string{"blog", "blog-web"})
	if len(conflicts) != 2 {
		t.Fatalf("conflicts = %v, want one container and one project conflict", conflicts)
	}
	if !strings.Contains(strings.Join(conflicts, "\n"), "container") ||
		!strings.Contains(strings.Join(conflicts, "\n"), "Compose project") {
		t.Errorf("conflicts do not explain both kinds: %v", conflicts)
	}

	if got := detectRestoreTargetConflicts(containers, "other", []string{"other"}); len(got) != 0 {
		t.Errorf("unrelated target conflicts = %v, want none", got)
	}
}

func TestBuildRestorePreflightScriptRejectsControlCharacters(t *testing.T) {
	_, err := BuildRestorePreflightScript(restorePreflightInput{
		ComposeFile: "/opt/blog\nwhoami/compose.yaml",
	})
	if err == nil || !strings.Contains(err.Error(), "control character") {
		t.Fatalf("error = %v, want a control-character rejection", err)
	}
}

// Run the generated script against a fake Docker CLI and a temp package. This
// catches shell syntax mistakes and proves the failure report survives a real
// bash execution rather than only string assertions.
func TestRestorePreflightScriptExecutesAndReportsMissingFiles(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	root := t.TempDir()
	binDir := t.TempDir()
	dockerStub := `#!/bin/sh
case "$1" in
  info) exit 0 ;;
  ps)
    if [ "$2" = "-a" ]; then
      printf '%s\n' '{"ID":"1","Names":"blog-web","Image":"nginx","Labels":"com.docker.compose.project=blog,com.docker.compose.service=web"}'
    fi
    exit 0
    ;;
  image) exit 0 ;;
  compose) exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(dockerStub), 0o755); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}

	script, err := BuildRestorePreflightScript(restorePreflightInput{
		ProjectName:        "blog",
		ComposeFile:        filepath.Join(root, "compose.yaml"),
		VolumeArchives:     []string{filepath.Join(root, "uploads.tar")},
		DatabaseDump:       filepath.Join(root, "blog.sql.gz"),
		ExpectedContainers: []string{"blog-web"},
		CrossMachine:       true,
	})
	if err != nil {
		t.Fatalf("BuildRestorePreflightScript() error = %v", err)
	}

	cmd := exec.Command(bashPath, "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("preflight script failed: %v\n%s", runErr, out)
	}

	report, err := ParseRestorePreflightOutput(string(out))
	if err != nil {
		t.Fatalf("ParseRestorePreflightOutput() error = %v\n%s", err, out)
	}
	if len(report.Errors) != 3 {
		t.Fatalf("errors = %v, want compose/archive/dump path failures\n%s", report.Errors, out)
	}
	if len(report.Containers) != 1 || report.Containers[0].Name != "blog-web" {
		t.Fatalf("containers = %+v, want the docker ps summary parsed\n%s", report.Containers, out)
	}
}
