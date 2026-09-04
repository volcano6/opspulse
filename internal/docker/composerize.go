package docker

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ComposeConfig represents the root of a compose.yaml document.
type ComposeConfig struct {
	Services map[string]ComposeService `yaml:"services"`
	Volumes  map[string]any            `yaml:"volumes,omitempty"`
}

// ComposeService represents a single service definition in compose.yaml.
type ComposeService struct {
	ContainerName string   `yaml:"container_name,omitempty"`
	Image         string   `yaml:"image"`
	Restart       string   `yaml:"restart,omitempty"`
	Ports         []string `yaml:"ports,omitempty"`
	Environment   []string `yaml:"environment,omitempty"`
	Volumes       []string `yaml:"volumes,omitempty"`
	WorkingDir    string   `yaml:"working_dir,omitempty"`
	Command       any      `yaml:"command,omitempty"`
	NetworkMode   string   `yaml:"network_mode,omitempty"`
}

// GenerateComposeYAML translates ContainerInfo into a standard compose.yaml document.
// If aliasName is provided, it overrides the service and container name.
func GenerateComposeYAML(info *ContainerInfo, aliasName string) (string, error) {
	if info == nil {
		return "", fmt.Errorf("container info is nil")
	}

	serviceName := info.Name
	if aliasName != "" {
		serviceName = aliasName
	}
	if serviceName == "" {
		serviceName = "app"
	}

	// Clean service name for YAML key (lowercase, alphanumeric, hyphens, underscores)
	serviceKey := sanitizeServiceName(serviceName)

	service := ComposeService{
		ContainerName: serviceName,
		Image:         info.Image,
	}

	// Restart policy
	rp := strings.TrimSpace(info.RestartPolicy)
	if rp != "" && rp != "no" {
		service.Restart = rp
	}

	// Working dir
	if info.WorkingDir != "" && info.WorkingDir != "/" {
		service.WorkingDir = info.WorkingDir
	}

	// Network mode (omit if default "bridge" or "default")
	netMode := strings.TrimSpace(info.NetworkMode)
	if netMode != "" && netMode != "default" && netMode != "bridge" {
		service.NetworkMode = netMode
	}

	// Filter and sort environment variables
	var filteredEnv []string
	for _, env := range info.Env {
		// Filter out standard base image path
		if strings.HasPrefix(env, "PATH=") {
			continue
		}
		filteredEnv = append(filteredEnv, env)
	}
	sort.Strings(filteredEnv)
	if len(filteredEnv) > 0 {
		service.Environment = filteredEnv
	}

	// Ports
	var portStrings []string
	for _, p := range info.Ports {
		str := p.String()
		if str != "" {
			portStrings = append(portStrings, str)
		}
	}
	sort.Strings(portStrings)
	if len(portStrings) > 0 {
		service.Ports = portStrings
	}

	// Volumes
	namedVolumes := make(map[string]any)
	var volumeStrings []string
	for _, m := range info.Mounts {
		str := m.String()
		if str != "" {
			volumeStrings = append(volumeStrings, str)
		}
		// Check if it's a named volume
		if isNamedVolume(m) {
			namedVolumes[m.Source] = nil
		}
	}
	sort.Strings(volumeStrings)
	if len(volumeStrings) > 0 {
		service.Volumes = volumeStrings
	}

	// Command
	if len(info.Command) > 0 {
		service.Command = info.Command
	}

	cfg := ComposeConfig{
		Services: map[string]ComposeService{
			serviceKey: service,
		},
	}

	if len(namedVolumes) > 0 {
		cfg.Volumes = namedVolumes
	}

	yamlBytes, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal compose YAML: %w", err)
	}

	return string(yamlBytes), nil
}

func sanitizeServiceName(name string) string {
	name = strings.ToLower(name)
	var sb strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('-')
		}
	}
	res := strings.Trim(sb.String(), "-")
	if res == "" {
		return "app"
	}
	return res
}

func isNamedVolume(m VolumeMount) bool {
	if m.Type == "volume" {
		return true
	}
	// If source is not an absolute path or relative path, it's a named volume
	src := m.Source
	if strings.HasPrefix(src, "/") || strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") || strings.HasPrefix(src, "~") {
		return false
	}
	// On Windows, drive letters like C:\ or D:\
	if len(src) >= 2 && src[1] == ':' {
		return false
	}
	return src != ""
}
