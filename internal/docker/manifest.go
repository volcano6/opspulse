package docker

import (
	"fmt"
	"path"

	"github.com/volcano6/opspulse/internal/shellquote"
	"gopkg.in/yaml.v3"
)

// ManifestFileName is the standard manifest file name in container backup packages.
const ManifestFileName = "manifest.yaml"

// ScratchDirName is the OpsPulse-owned working directory inside a container
// project directory. Hot dumps, volume archives and manifest.yaml all live
// under it so that cleanup can never touch the operator's own "dumps/" or
// "volumes/" directories, which are common live-data locations in Compose
// projects (e.g. "./volumes/db:/var/lib/postgresql/data").
const ScratchDirName = ".opspulse"

// ScratchDir returns the OpsPulse scratch directory for a project directory.
func ScratchDir(projectDir string) string {
	return path.Join(projectDir, ScratchDirName)
}

// ScratchDumpsDir returns the hot-dump directory inside the scratch directory.
func ScratchDumpsDir(projectDir string) string {
	return path.Join(ScratchDir(projectDir), "dumps")
}

// ScratchVolumesDir returns the volume-archive directory inside the scratch directory.
func ScratchVolumesDir(projectDir string) string {
	return path.Join(ScratchDir(projectDir), "volumes")
}

// ManifestPath returns the container manifest location for new snapshots,
// i.e. "<projectDir>/.opspulse/manifest.yaml".
func ManifestPath(projectDir string) string {
	return path.Join(ScratchDir(projectDir), ManifestFileName)
}

// LegacyManifestPath returns the manifest location used by snapshots taken
// before artifacts were namespaced under ScratchDirName. Restore must still
// accept it for as long as such snapshots exist in repositories.
func LegacyManifestPath(projectDir string) string {
	return path.Join(projectDir, ManifestFileName)
}

// ContainerManifest defines the structured, self-describing metadata for a backed-up container application package.
type ContainerManifest struct {
	FormatVersion  int                     `json:"format_version" yaml:"format_version"`
	App            string                  `json:"app" yaml:"app"`
	ComposeFile    string                  `json:"compose_file" yaml:"compose_file"` // typically "compose.yaml"
	Volumes        []ManifestVolume        `json:"volumes,omitempty" yaml:"volumes,omitempty"`
	ExternalMounts []ManifestExternalMount `json:"external_mounts,omitempty" yaml:"external_mounts,omitempty"`
	Database       *ManifestDatabase       `json:"database,omitempty" yaml:"database,omitempty"`
}

// ManifestVolume represents a named volume archived as tar inside the package scratch directory.
type ManifestVolume struct {
	OriginalName string `json:"original_name" yaml:"original_name"`
	Archive      string `json:"archive" yaml:"archive"` // relative to the package root, e.g. "volumes/my-vol/data.tar"
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
//
// The archive file name is passed through shellquote as part of the whole
// helper command, so a file name carrying shell metacharacters cannot break
// out of the `sh -c` argument.
func BuildVolumeExportScript(volumeName, hostArchivePath string) string {
	dir := path.Dir(hostArchivePath)
	file := path.Base(hostArchivePath)
	return fmt.Sprintf(`mkdir -p %s
HELPER_IMG="${OPSPULSE_HELPER_IMAGE:-alpine:3.20}"
if ! docker image inspect "$HELPER_IMG" >/dev/null 2>&1; then
  if docker image inspect busybox:1.36 >/dev/null 2>&1; then
    HELPER_IMG="busybox:1.36"
  elif docker image inspect alpine >/dev/null 2>&1; then
    HELPER_IMG="alpine"
  elif docker image inspect busybox >/dev/null 2>&1; then
    HELPER_IMG="busybox"
  fi
fi
docker run --rm -v %s:/src:ro -v %s:/dst "$HELPER_IMG" sh -c %s`,
		shellquote.Quote(dir),
		shellquote.Quote(volumeName),
		shellquote.Quote(dir),
		shellquote.Quote(fmt.Sprintf("cd /src && tar cpf /dst/%s .", file)),
	)
}

// BuildVolumeImportScript generates a bash script to restore a tar archive into a Docker named volume.
//
// As with BuildVolumeExportScript, the archive file name is shellquoted as
// part of the whole helper command.
func BuildVolumeImportScript(volumeName, hostArchivePath string) string {
	dir := path.Dir(hostArchivePath)
	file := path.Base(hostArchivePath)
	return fmt.Sprintf(`HELPER_IMG="${OPSPULSE_HELPER_IMAGE:-alpine:3.20}"
if ! docker image inspect "$HELPER_IMG" >/dev/null 2>&1; then
  if docker image inspect busybox:1.36 >/dev/null 2>&1; then
    HELPER_IMG="busybox:1.36"
  elif docker image inspect alpine >/dev/null 2>&1; then
    HELPER_IMG="alpine"
  elif docker image inspect busybox >/dev/null 2>&1; then
    HELPER_IMG="busybox"
  fi
fi
docker volume create %s >/dev/null
docker run --rm -v %s:/dst -v %s:/src:ro "$HELPER_IMG" sh -c %s`,
		shellquote.Quote(volumeName),
		shellquote.Quote(volumeName),
		shellquote.Quote(dir),
		shellquote.Quote(fmt.Sprintf("cd /dst && tar xpf /src/%s", file)),
	)
}
