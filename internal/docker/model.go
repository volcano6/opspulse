// Package docker provides Docker container metadata inspection, reverse Compose translation, and database hooks.
package docker

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

var (
	// ErrEmptyInspectOutput is returned when docker inspect output is empty.
	ErrEmptyInspectOutput = errors.New("empty docker inspect output")
	// ErrInvalidInspectJSON is returned when docker inspect output cannot be parsed.
	ErrInvalidInspectJSON = errors.New("invalid docker inspect JSON output")
	// ErrContainerNotFound is returned when no container is found in inspect output.
	ErrContainerNotFound = errors.New("container not found in inspect output")
)

// Standard Docker Compose label keys.
const (
	LabelComposeProject     = "com.docker.compose.project"
	LabelComposeService     = "com.docker.compose.service"
	LabelComposeWorkingDir  = "com.docker.compose.project.working_dir"
	LabelComposeConfigFiles = "com.docker.compose.project.config_files"
)

// PortMapping represents a port published from the container to the host.
type PortMapping struct {
	HostIP        string `json:"host_ip,omitempty" yaml:"host_ip,omitempty"`
	HostPort      string `json:"host_port" yaml:"host_port"`
	ContainerPort string `json:"container_port" yaml:"container_port"`
	Protocol      string `json:"protocol,omitempty" yaml:"protocol,omitempty"` // "tcp" or "udp"
}

// String formats the port mapping in standard Docker/Compose format (e.g. "8080:80" or "127.0.0.1:8080:80/udp").
func (p PortMapping) String() string {
	proto := ""
	if p.Protocol != "" && strings.ToLower(p.Protocol) != "tcp" {
		proto = "/" + strings.ToLower(p.Protocol)
	}

	host := p.HostPort
	if p.HostIP != "" && p.HostIP != "0.0.0.0" && p.HostIP != "::" {
		host = p.HostIP + ":" + p.HostPort
	}

	if host != "" {
		return fmt.Sprintf("%s:%s%s", host, p.ContainerPort, proto)
	}
	return fmt.Sprintf("%s%s", p.ContainerPort, proto)
}

// VolumeMount represents a bind mount or named volume attached to the container.
type VolumeMount struct {
	Type        string `json:"type" yaml:"type"` // "bind" or "volume"
	Name        string `json:"name,omitempty" yaml:"name,omitempty"`
	Source      string `json:"source" yaml:"source"`
	HostPath    string `json:"host_path,omitempty" yaml:"host_path,omitempty"`
	Destination string `json:"destination" yaml:"destination"`
	ReadOnly    bool   `json:"read_only,omitempty" yaml:"read_only,omitempty"`
}

// String formats the volume mapping in standard Docker/Compose format (e.g. "/host/path:/container/path:ro").
func (v VolumeMount) String() string {
	mode := ""
	if v.ReadOnly {
		mode = ":ro"
	}
	return fmt.Sprintf("%s:%s%s", v.Source, v.Destination, mode)
}

// ContainerInfo captures inspected runtime metadata of a Docker container.
type ContainerInfo struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	WorkingDir    string            `json:"working_dir,omitempty"`
	Command       []string          `json:"command,omitempty"`
	Entrypoint    []string          `json:"entrypoint,omitempty"`
	Env           []string          `json:"env,omitempty"`
	RestartPolicy string            `json:"restart_policy,omitempty"`
	NetworkMode   string            `json:"network_mode,omitempty"`
	Ports         []PortMapping     `json:"ports,omitempty"`
	Mounts        []VolumeMount     `json:"mounts,omitempty"`
	Devices       []string          `json:"devices,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

// IsCompose returns true if this container was created and managed by Docker Compose.
func (c *ContainerInfo) IsCompose() bool {
	return c.Labels[LabelComposeProject] != ""
}

// ComposeProject returns the project name if managed by Docker Compose.
func (c *ContainerInfo) ComposeProject() string {
	return c.Labels[LabelComposeProject]
}

// ComposeService returns the service name if managed by Docker Compose.
func (c *ContainerInfo) ComposeService() string {
	return c.Labels[LabelComposeService]
}

// ComposeWorkingDir returns the working directory of the Compose project on the host.
func (c *ContainerInfo) ComposeWorkingDir() string {
	return c.Labels[LabelComposeWorkingDir]
}

// IsDatabase returns true if the container's image indicates a recognized SQL database.
func (c *ContainerInfo) IsDatabase() bool {
	return c.DatabaseEngine() != ""
}

// DatabaseEngine identifies the database dialect ("mysql" or "postgres") if recognized.
func (c *ContainerInfo) DatabaseEngine() string {
	img := strings.ToLower(c.Image)
	// Match image names like "mysql", "mysql:8", "mariadb:10", "library/postgres:16-alpine", "bitnami/postgresql"
	if strings.Contains(img, "mysql") || strings.Contains(img, "mariadb") {
		return "mysql"
	}
	if strings.Contains(img, "postgres") {
		return "postgres"
	}
	return ""
}

// IsSystemMount returns true if the mount source represents a host system socket,
// virtual filesystem, or runtime-injected configuration that should not be archived.
func IsSystemMount(source string) bool {
	clean := path.Clean(strings.ReplaceAll(source, "\\", "/"))
	if clean == "/var/run/docker.sock" || clean == "/run/docker.sock" {
		return true
	}
	if clean == "/etc/localtime" || clean == "/etc/timezone" || clean == "/etc/resolv.conf" || clean == "/etc/hosts" || clean == "/etc/hostname" {
		return true
	}
	if strings.HasPrefix(clean, "/proc") || strings.HasPrefix(clean, "/sys") || strings.HasPrefix(clean, "/dev") || strings.HasPrefix(clean, "/run") {
		return true
	}
	return false
}

// IsDatabaseDataDir returns true if the container is a database and the destination
// matches standard physical database storage locations.
func (c *ContainerInfo) IsDatabaseDataDir(destination string) bool {
	if !c.IsDatabase() {
		return false
	}
	clean := path.Clean(strings.ReplaceAll(destination, "\\", "/"))
	return clean == "/var/lib/mysql" ||
		clean == "/var/lib/postgresql/data" ||
		clean == "/var/lib/postgresql" ||
		clean == "/bitnami/mariadb/data" ||
		clean == "/bitnami/mysql/data" ||
		clean == "/bitnami/postgresql/data"
}

// Raw inspect structures used for unmarshaling `docker inspect` JSON.
type rawInspectContainer struct {
	ID         string `json:"Id"`
	Name       string `json:"Name"`
	Config     rawConfig
	HostConfig rawHostConfig
	Mounts     []rawMount
}

type rawConfig struct {
	Image      string            `json:"Image"`
	Cmd        []string          `json:"Cmd"`
	Entrypoint []string          `json:"Entrypoint"`
	Env        []string          `json:"Env"`
	WorkingDir string            `json:"WorkingDir"`
	Labels     map[string]string `json:"Labels"`
}

type rawHostConfig struct {
	RestartPolicy struct {
		Name              string `json:"Name"`
		MaximumRetryCount int    `json:"MaximumRetryCount"`
	} `json:"RestartPolicy"`
	PortBindings map[string][]rawPortBinding `json:"PortBindings"`
	Binds        []string                    `json:"Binds"`
	NetworkMode  string                      `json:"NetworkMode"`
	Devices      []rawDevice                 `json:"Devices"`
}

type rawDevice struct {
	PathOnHost        string `json:"PathOnHost"`
	PathInContainer   string `json:"PathInContainer"`
	CgroupPermissions string `json:"CgroupPermissions"`
}

type rawPortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type rawMount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	Mode        string `json:"Mode"`
	RW          bool   `json:"RW"`
}

// ParseInspectJSON parses `docker inspect <container>` JSON output into a structured ContainerInfo.
func ParseInspectJSON(data []byte) (*ContainerInfo, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, ErrEmptyInspectOutput
	}

	var list []rawInspectContainer
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidInspectJSON, err)
		}
	} else {
		var single rawInspectContainer
		if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidInspectJSON, err)
		}
		list = append(list, single)
	}

	if len(list) == 0 {
		return nil, ErrContainerNotFound
	}

	raw := list[0]
	name := strings.TrimPrefix(raw.Name, "/")

	info := &ContainerInfo{
		ID:            raw.ID,
		Name:          name,
		Image:         raw.Config.Image,
		WorkingDir:    raw.Config.WorkingDir,
		Command:       raw.Config.Cmd,
		Entrypoint:    raw.Config.Entrypoint,
		Env:           raw.Config.Env,
		RestartPolicy: raw.HostConfig.RestartPolicy.Name,
		NetworkMode:   raw.HostConfig.NetworkMode,
		Labels:        raw.Config.Labels,
	}

	if info.Labels == nil {
		info.Labels = make(map[string]string)
	}

	// Parse Ports from PortBindings
	for portProto, bindings := range raw.HostConfig.PortBindings {
		parts := strings.Split(portProto, "/")
		port := parts[0]
		proto := "tcp"
		if len(parts) > 1 {
			proto = parts[1]
		}

		if len(bindings) == 0 {
			info.Ports = append(info.Ports, PortMapping{
				ContainerPort: port,
				Protocol:      proto,
			})
			continue
		}

		for _, b := range bindings {
			info.Ports = append(info.Ports, PortMapping{
				HostIP:        b.HostIP,
				HostPort:      b.HostPort,
				ContainerPort: port,
				Protocol:      proto,
			})
		}
	}

	// Parse Devices from HostConfig
	for _, d := range raw.HostConfig.Devices {
		if d.PathOnHost != "" {
			devStr := d.PathOnHost
			if d.PathInContainer != "" {
				devStr = fmt.Sprintf("%s:%s", d.PathOnHost, d.PathInContainer)
			}
			if d.CgroupPermissions != "" {
				devStr = fmt.Sprintf("%s:%s", devStr, d.CgroupPermissions)
			}
			info.Devices = append(info.Devices, devStr)
		}
	}

	// Parse Mounts
	seenDests := make(map[string]bool)
	for _, m := range raw.Mounts {
		src := m.Source
		name := ""
		hostPath := m.Source
		if m.Type == "volume" && m.Name != "" {
			name = m.Name
			src = m.Name
		}
		mountType := m.Type
		if mountType == "" {
			mountType = "bind"
		}
		info.Mounts = append(info.Mounts, VolumeMount{
			Type:        mountType,
			Name:        name,
			Source:      src,
			HostPath:    hostPath,
			Destination: m.Destination,
			ReadOnly:    !m.RW || strings.Contains(m.Mode, "ro"),
		})
		seenDests[m.Destination] = true
	}

	// Fallback to HostConfig.Binds if Mounts didn't capture them
	for _, b := range raw.HostConfig.Binds {
		parts := strings.Split(b, ":")
		if len(parts) >= 2 {
			src := parts[0]
			dst := parts[1]
			ro := false
			if len(parts) >= 3 && strings.Contains(parts[2], "ro") {
				ro = true
			}
			if !seenDests[dst] {
				info.Mounts = append(info.Mounts, VolumeMount{
					Type:        "bind",
					Source:      src,
					Destination: dst,
					ReadOnly:    ro,
				})
				seenDests[dst] = true
			}
		}
	}

	return info, nil
}
