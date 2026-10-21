package docker

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/volcano6/opspulse/internal/shellquote"
	"gopkg.in/yaml.v3"
)

// ManifestFileName is the standard manifest file name in container backup packages.
const ManifestFileName = "manifest.yaml"

// ManifestFormatVersion is the only container-manifest schema this build can
// interpret. Unknown versions must be rejected rather than guessed at.
const ManifestFormatVersion = 1

var (
	// ErrUnsupportedManifestVersion reports a manifest written by a newer or
	// incompatible OpsPulse build.
	ErrUnsupportedManifestVersion = errors.New("unsupported container manifest format version")
	// ErrInvalidManifest reports malformed or unsafe manifest content.
	ErrInvalidManifest = errors.New("invalid container manifest")
)

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
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate enforces the manifest schema and the package-relative path
// boundary. Every artifact path is later joined with the package root and fed
// to a shell script, so accepting "../" here would let a crafted manifest read
// a file outside the restored package.
func (m *ContainerManifest) Validate() error {
	if m == nil {
		return fmt.Errorf("%w: manifest is nil", ErrInvalidManifest)
	}
	if m.FormatVersion != ManifestFormatVersion {
		return fmt.Errorf(
			"%w: got %d, want %d",
			ErrUnsupportedManifestVersion,
			m.FormatVersion,
			ManifestFormatVersion,
		)
	}
	if err := validateManifestField("app", m.App); err != nil {
		return err
	}
	if strings.TrimSpace(m.ComposeFile) == "" {
		return fmt.Errorf("%w: compose_file is required", ErrInvalidManifest)
	}
	if err := validatePackageRelativePath("compose_file", m.ComposeFile); err != nil {
		return err
	}
	for i, v := range m.Volumes {
		if err := validateManifestField(fmt.Sprintf("volumes[%d].original_name", i), v.OriginalName); err != nil {
			return err
		}
		if strings.TrimSpace(v.Archive) == "" {
			return fmt.Errorf("%w: volumes[%d].archive is required", ErrInvalidManifest, i)
		}
		if err := validatePackageRelativePath(fmt.Sprintf("volumes[%d].archive", i), v.Archive); err != nil {
			return err
		}
		if err := validateManifestField(fmt.Sprintf("volumes[%d].target", i), v.Target); err != nil {
			return err
		}
	}
	for i, em := range m.ExternalMounts {
		if err := validateManifestField(fmt.Sprintf("external_mounts[%d].source", i), em.Source); err != nil {
			return err
		}
		if err := validateManifestField(fmt.Sprintf("external_mounts[%d].target", i), em.Target); err != nil {
			return err
		}
		if hasControlCharacter(em.Reason) {
			return fmt.Errorf("%w: external_mounts[%d].reason contains a control character", ErrInvalidManifest, i)
		}
	}
	if m.Database != nil {
		if _, err := normalizeManifestEngine(m.Database.Engine); err != nil {
			return err
		}
		if err := validateManifestField("database.container", m.Database.Container); err != nil {
			return err
		}
		if m.Database.Dump != "" {
			if err := validatePackageRelativePath("database.dump", m.Database.Dump); err != nil {
				return err
			}
		}
	}
	return nil
}

// ResolvePath joins a validated package-relative path with root and verifies
// that the result stays under root. Validate already rejects traversal, but
// this second boundary keeps callers safe if a manifest is constructed in Go
// instead of parsed from YAML.
func (m *ContainerManifest) ResolvePath(root, name, relative string) (string, error) {
	if err := validatePackageRelativePath(name, relative); err != nil {
		return "", err
	}
	cleanRoot := path.Clean(root)
	resolved := path.Clean(path.Join(cleanRoot, relative))
	if !isWithinRoot(cleanRoot, resolved) {
		return "", fmt.Errorf("%w: %s %q escapes package root %q", ErrInvalidManifest, name, relative, root)
	}
	return resolved, nil
}

// ResolveVolumeArchive resolves one volume archive under the package root.
func (m *ContainerManifest) ResolveVolumeArchive(root string, index int) (string, error) {
	if index < 0 || index >= len(m.Volumes) {
		return "", fmt.Errorf("%w: volume index %d out of range", ErrInvalidManifest, index)
	}
	return m.ResolvePath(root, fmt.Sprintf("volumes[%d].archive", index), m.Volumes[index].Archive)
}

// ResolveDatabaseDump resolves the database dump under the package root.
func (m *ContainerManifest) ResolveDatabaseDump(root string) (string, error) {
	if m.Database == nil {
		return "", fmt.Errorf("%w: database section is missing", ErrInvalidManifest)
	}
	return m.ResolvePath(root, "database.dump", m.Database.Dump)
}

func validateManifestField(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidManifest, name)
	}
	if hasControlCharacter(value) {
		return fmt.Errorf("%w: %s contains a control character", ErrInvalidManifest, name)
	}
	return nil
}

func hasControlCharacter(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// validatePackageRelativePath accepts only a normalized path below the package
// root. Both '/' and '\' are treated as separators because manifests can be
// copied between Windows and Linux, and filepath.Clean on Windows would
// otherwise hide a traversal that the target shell still interprets.
func validatePackageRelativePath(name, value string) error {
	if err := validateManifestField(name, value); err != nil {
		return err
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) {
		return fmt.Errorf("%w: %s %q must be relative to the package root", ErrInvalidManifest, name, value)
	}
	if len(value) >= 2 && value[1] == ':' {
		return fmt.Errorf("%w: %s %q must not contain a drive prefix", ErrInvalidManifest, name, value)
	}
	normalized := strings.ReplaceAll(value, `\`, "/")
	if path.IsAbs(normalized) || path.Clean(normalized) != normalized || normalized == "." || normalized == ".." ||
		strings.HasPrefix(normalized, "../") || strings.Contains(normalized, "/../") {
		return fmt.Errorf("%w: %s %q must be a clean path inside the package", ErrInvalidManifest, name, value)
	}
	return nil
}

func isWithinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.FromSlash(root), filepath.FromSlash(candidate))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func normalizeManifestEngine(engine string) (string, error) {
	normalized, err := NormalizeDatabaseEngine(engine)
	if err != nil {
		return "", fmt.Errorf("%w: database.engine: %v", ErrInvalidManifest, err)
	}
	return normalized, nil
}

// helperImagePrologue resolves the helper image to use for volume scripts. It
// honours OPSPULSE_HELPER_IMAGE and falls back through a list of known-good
// images, so the generated script works on hosts with no network access.
const helperImagePrologue = `HELPER_IMG="${OPSPULSE_HELPER_IMAGE:-alpine:3.20}"
if ! docker image inspect "$HELPER_IMG" >/dev/null 2>&1; then
  if docker image inspect busybox:1.36 >/dev/null 2>&1; then
    HELPER_IMG="busybox:1.36"
  elif docker image inspect alpine >/dev/null 2>&1; then
    HELPER_IMG="alpine"
  elif docker image inspect busybox >/dev/null 2>&1; then
    HELPER_IMG="busybox"
  fi
fi`

// BuildVolumeExportScript generates a bash script to archive a Docker named volume into a tar file on the host.
//
// The archive file name is passed through shellquote as part of the whole
// helper command, so a file name carrying shell metacharacters cannot break
// out of the `sh -c` argument.
func BuildVolumeExportScript(volumeName, hostArchivePath string) string {
	dir := path.Dir(hostArchivePath)
	file := path.Base(hostArchivePath)
	return fmt.Sprintf(`mkdir -p %s
%s
docker run --rm -v %s:/src:ro -v %s:/dst "$HELPER_IMG" sh -c %s`,
		shellquote.Quote(dir),
		helperImagePrologue,
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
	return fmt.Sprintf(`%s
docker volume create %s >/dev/null
docker run --rm -v %s:/dst -v %s:/src:ro "$HELPER_IMG" sh -c %s`,
		helperImagePrologue,
		shellquote.Quote(volumeName),
		shellquote.Quote(volumeName),
		shellquote.Quote(dir),
		shellquote.Quote(fmt.Sprintf("cd /dst && tar xpf /src/%s", file)),
	)
}
