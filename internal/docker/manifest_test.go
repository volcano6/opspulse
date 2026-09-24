package docker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifest_MarshalUnmarshal(t *testing.T) {
	manifest := &ContainerManifest{
		FormatVersion: 1,
		App:           "my-app",
		ComposeFile:   "compose.yaml",
		Volumes: []ManifestVolume{
			{
				OriginalName: "app-data",
				Archive:      "volumes/app-data/data.tar",
				Target:       "/var/lib/app",
			},
		},
		ExternalMounts: []ManifestExternalMount{
			{
				Source:   "/var/run/docker.sock",
				Target:   "/var/run/docker.sock",
				Required: true,
				Reason:   "unix_socket",
			},
		},
		Database: &ManifestDatabase{
			Engine:    "mysql",
			Container: "db-service",
			Dump:      "dumps/database.sql.gz",
		},
	}

	yamlStr, err := MarshalManifest(manifest)
	if err != nil {
		t.Fatalf("MarshalManifest failed: %v", err)
	}

	parsed, err := UnmarshalManifest([]byte(yamlStr))
	if err != nil {
		t.Fatalf("UnmarshalManifest failed: %v", err)
	}

	if parsed.App != "my-app" || parsed.FormatVersion != 1 {
		t.Errorf("unexpected parsed manifest header: %+v", parsed)
	}
	if len(parsed.Volumes) != 1 || parsed.Volumes[0].OriginalName != "app-data" {
		t.Errorf("unexpected parsed volumes: %+v", parsed.Volumes)
	}
	if len(parsed.ExternalMounts) != 1 || parsed.ExternalMounts[0].Source != "/var/run/docker.sock" {
		t.Errorf("unexpected parsed external mounts: %+v", parsed.ExternalMounts)
	}
	if parsed.Database == nil || parsed.Database.Engine != "mysql" || parsed.Database.Dump != "dumps/database.sql.gz" {
		t.Errorf("unexpected parsed database: %+v", parsed.Database)
	}
}

func TestVolumeScripts(t *testing.T) {
	exportScript := BuildVolumeExportScript("my-vol", "/var/lib/opspulse/containers/app/volumes/my-vol/data.tar")
	if exportScript == "" {
		t.Fatal("expected non-empty export script")
	}
	if !containsAll(exportScript, "my-vol", "data.tar", "tar cpf") {
		t.Errorf("export script missing required elements: %s", exportScript)
	}

	importScript := BuildVolumeImportScript("my-vol", "/var/lib/opspulse/containers/app/volumes/my-vol/data.tar")
	if importScript == "" {
		t.Fatal("expected non-empty import script")
	}
	if !containsAll(importScript, "my-vol", "docker volume create", "tar xpf") {
		t.Errorf("import script missing required elements: %s", importScript)
	}
}

func containsAll(s string, substrings ...string) bool {
	for _, sub := range substrings {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// runWithStubDocker executes a generated script against a stub `docker` that
// echoes its argv, so the test observes what the helper container would
// actually receive.
func runWithStubDocker(t *testing.T, script string) string {
	t.Helper()
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	binDir := t.TempDir()
	stub := `#!/bin/sh
case "$1" in
  image|volume) exit 0 ;;
esac
for a in "$@"; do printf 'DARG:%s\n' "$a"; done
`
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(stub), 0o755); err != nil { // #nosec G306 -- test stub
		t.Fatalf("write docker stub: %v", err)
	}

	cmd := exec.Command(shell, "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated script failed: %v\n%s", err, out)
	}
	return string(out)
}

// TestVolumeScripts_FileNameIsShellQuoted covers a name containing both a space
// and a single quote. The file name used to be interpolated into a single-quoted
// `sh -c '...'` argument, so such a name escaped the quoting and split the
// command into several arguments.
func TestVolumeScripts_FileNameIsShellQuoted(t *testing.T) {
	const weird = "we ird'name.tar"
	base := "/var/lib/opspulse/containers/app/.opspulse/volumes/my-vol"

	exportOut := runWithStubDocker(t, BuildVolumeExportScript("my-vol", base+"/"+weird))
	wantExport := "DARG:cd /src && tar cpf /dst/" + weird + " ."
	if !strings.Contains(exportOut, wantExport+"\n") {
		t.Errorf("archive name was not passed as one intact argument, want line %q; got:\n%s", wantExport, exportOut)
	}
	if !strings.Contains(exportOut, "DARG:"+base+":/dst\n") {
		t.Errorf("archive directory argument missing or malformed; got:\n%s", exportOut)
	}

	importOut := runWithStubDocker(t, BuildVolumeImportScript("my-vol", base+"/"+weird))
	wantImport := "DARG:cd /dst && tar xpf /src/" + weird
	if !strings.Contains(importOut, wantImport+"\n") {
		t.Errorf("archive name was not passed as one intact argument, want line %q; got:\n%s", wantImport, importOut)
	}

	// The old behaviour split the argument, leaving the bare file name as its
	// own argv entry. That must not happen.
	for name, out := range map[string]string{"export": exportOut, "import": importOut} {
		if strings.Contains(out, "DARG:name.tar\n") {
			t.Errorf("%s script leaked a split file name; got:\n%s", name, out)
		}
	}
}
