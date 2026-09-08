package docker

import (
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
