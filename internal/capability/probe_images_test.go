package capability

import (
	"strings"
	"testing"
)

func TestBuildProbeScriptChecksImagesReadOnly(t *testing.T) {
	script, err := BuildProbeScript(ProbeOptions{Images: []string{"mysql:8.0", "ghcr.io/acme/web:1.2"}})
	if err != nil {
		t.Fatalf("BuildProbeScript() error: %v", err)
	}
	assertReadOnlyScript(t, "probe script", script)
	for _, want := range []string{"docker image inspect", "mysql:8.0", "ghcr.io/acme/web:1.2"} {
		if !strings.Contains(script, want) {
			t.Errorf("probe script missing %q:\n%s", want, script)
		}
	}
}

func TestBuildProbeScriptRejectsPipeImage(t *testing.T) {
	if _, err := BuildProbeScript(ProbeOptions{Images: []string{"mysql:8.0|rm -rf /"}}); err == nil {
		t.Fatal("BuildProbeScript() should reject an image containing '|'")
	}
}

func TestParseProbeOutputImages(t *testing.T) {
	raw := strings.Join([]string{
		"---OPSPULSE-CAPABILITY---",
		"DOCKER=present",
		"DOCKER_DAEMON=running",
		"IMAGE=mysql:8.0|present",
		"IMAGE=nginx:alpine|missing",
		"---OPSPULSE-CAPABILITY-END---",
	}, "\n")

	report := ParseProbeOutput("vps-b", raw)
	if len(report.Images) != 2 {
		t.Fatalf("images = %+v, want two entries", report.Images)
	}
	if report.Images[0].Name != "mysql:8.0" || report.Images[0].Status != StatusOK {
		t.Errorf("images[0] = %+v, want a present mysql image", report.Images[0])
	}
	if report.Images[1].Status != StatusMissing {
		t.Errorf("images[1] = %+v, want a missing image", report.Images[1])
	}
	if !containsSubstring(report.Notes, "nginx:alpine") {
		t.Errorf("notes = %v, want a hint about the missing image", report.Notes)
	}
}

func containsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}
