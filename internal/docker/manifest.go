package docker

import (
	"fmt"
	"path"

	"github.com/volcano6/opspulse/internal/shellquote"
	"gopkg.in/yaml.v3"
)

// ManifestFileName is the standard manifest file name in container backup packages.
const ManifestFileName = "manifest.yaml"

// ContainerManifest defines the structured, self-describing metadata for a backed-up container application package.
type ContainerManifest struct {
	FormatVersion  int                     `json:"format_version" yaml:"format_version"`
	App            string                  `json:"app" yaml:"app"`
	ComposeFile    string                  `json:"compose_file" yaml:"compose_file"` // typically "compose.yaml"
	Volumes        []ManifestVolume        `json:"volumes,omitempty" yaml:"volumes,omitempty"`
	ExternalMounts []ManifestExternalMount `json:"external_mounts,omitempty" yaml:"external_mounts,omitempty"`
	Database       *ManifestDatabase       `json:"database,omitempty" yaml:"database,omitempty"`
}

// ManifestVolume represents a named volume archived as tar in the project directory.
type ManifestVolume struct {
	OriginalName string `json:"original_name" yaml:"original_name"`
	Archive      string `json:"archive" yaml:"archive"` // relative path inside project, e.g. "volumes/my-vol/data.tar"
	Target       string `json:"target" yaml:"target"`   // container target mount path, e.g. "/var/lib/app"
}

// ManifestExternalMount represents a host system/runtime dependency preserved by absolute path.
type ManifestExternalMount struct {
	Source   string `json:"source" yaml:"source"`
	Target   string `json:"target" yaml:"target"`
	Required bool   `json:"required" yaml:"required"`
	Reason   string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// ManifestDatabase holds database engine information and the logical hot dump location.
type ManifestDatabase struct {
	Engine    string `json:"engine" yaml:"engine"`
	Container string `json:"container" yaml:"container"`
	Dump      string `json:"dump" yaml:"dump"` // relative path inside project, e.g. "dumps/database.sql.gz"
}

// MarshalManifest serializes a ContainerManifest to YAML.
func MarshalManifest(m *ContainerManifest) (string, error) {
	data, err := yaml.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal manifest: %w", err)
	}
	return string(data), nil
}

// UnmarshalManifest parses a ContainerManifest from YAML bytes.
func UnmarshalManifest(data []byte) (*ContainerManifest, error) {
	var m ContainerManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("unmarshal manifest: %w", err)
	}
	return &m, nil
}

// BuildVolumeExportScript generates a bash script to archive a Docker named volume into a tar file on the host.
func BuildVolumeExportScript(volumeName, hostArchivePath string) string {
	dir := path.Dir(hostArchivePath)
	file := path.Base(hostArchivePath)
	return fmt.Sprintf(`mkdir -p %s
HELPER_IMG="alpine"
if ! docker image inspect alpine >/dev/null 2>&1; then
  if docker image inspect busybox >/dev/null 2>&1; then
    HELPER_IMG="busybox"
  fi
fi
docker run --rm -v %s:/src:ro -v %s:/dst "$HELPER_IMG" sh -c 'cd /src && tar cpf /dst/%s .'`,
		shellquote.Quote(dir),
		shellquote.Quote(volumeName),
		shellquote.Quote(dir),
		file,
	)
}

// BuildVolumeImportScript generates a bash script to restore a tar archive into a Docker named volume.
func BuildVolumeImportScript(volumeName, hostArchivePath string) string {
	dir := path.Dir(hostArchivePath)
	file := path.Base(hostArchivePath)
	return fmt.Sprintf(`HELPER_IMG="alpine"
if ! docker image inspect alpine >/dev/null 2>&1; then
  if docker image inspect busybox >/dev/null 2>&1; then
    HELPER_IMG="busybox"
  fi
fi
docker volume create %s >/dev/null
docker run --rm -v %s:/dst -v %s:/src:ro "$HELPER_IMG" sh -c 'cd /dst && tar xpf /src/%s'`,
		shellquote.Quote(volumeName),
		shellquote.Quote(volumeName),
		shellquote.Quote(dir),
		file,
	)
}
