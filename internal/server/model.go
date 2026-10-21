// Package server manages server inventory, metadata, and persistence.
package server

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/volcano6/opspulse/internal/secret"
)

var (
	// ErrServerNotFound is returned when a requested server does not exist.
	ErrServerNotFound = errors.New("server not found")
	// ErrInvalidServerName is returned when the server name is invalid.
	ErrInvalidServerName = errors.New("invalid server name")
	// ErrInvalidHost is returned when the server host is empty.
	ErrInvalidHost = errors.New("server host cannot be empty")
	// ErrInvalidPort is returned when a configured port is outside the TCP range.
	ErrInvalidPort = errors.New("invalid port")
	// ErrSelfReferencingJumpHost is returned when a server specifies itself as its jump host.
	ErrSelfReferencingJumpHost = errors.New("server cannot specify itself as jump host")
	// ErrJumpHostCycle is returned when a cycle is detected in jump host dependencies.
	ErrJumpHostCycle = errors.New("cyclic jump host dependency detected")
)

// ValidateServerName checks if a server name is a safe, valid identifier.
//
// A name may contain only ASCII letters, digits, and hyphens. It cannot be
// empty, start or end with a hyphen, carry leading/trailing whitespace, or
// contain path separators or colons. Underscores, dots, and '@' are rejected:
// one machine keeps one canonical spelling, and those characters either look
// like a hyphen in many fonts or collide with "user@host" target syntax.
func ValidateServerName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: cannot be empty", ErrInvalidServerName)
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("%w: cannot have leading or trailing whitespace", ErrInvalidServerName)
	}
	if len(name) > 255 {
		return fmt.Errorf("%w: name exceeds maximum length of 255 characters", ErrInvalidServerName)
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return fmt.Errorf("%w: cannot start or end with '-'", ErrInvalidServerName)
	}
	if strings.Contains(name, "_") {
		return fmt.Errorf("%w: %q contains '_'; use '-' instead", ErrInvalidServerName, name)
	}
	if strings.ContainsAny(name, "/\\:") {
		return fmt.Errorf("%w: cannot contain path separators or colons", ErrInvalidServerName)
	}
	if filepath.Base(name) != name || filepath.Clean(name) != name {
		return fmt.Errorf("%w: cannot contain path navigation characters", ErrInvalidServerName)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' {
			return fmt.Errorf("%w: %q contains invalid character %q; server names may contain only letters, digits, and hyphens", ErrInvalidServerName, name, r)
		}
	}
	return nil
}

// NormalizeServerName returns the canonical form of a server name: underscores
// become hyphens. ValidateServerName does not accept underscores, so callers
// apply this to names they are about to create, turning "web_1" into "web-1"
// before validation. Existing entries are never rewritten here; a one-off
// migration handles those.
func NormalizeServerName(name string) string {
	return strings.ReplaceAll(name, "_", "-")
}

// Server represents a managed server instance.
type Server struct {
	Name        string            `yaml:"name" json:"name"`
	Host        string            `yaml:"host" json:"host"`
	Port        int               `yaml:"port" json:"port"`
	User        string            `yaml:"user" json:"user"`
	KeyPath     string            `yaml:"key_path,omitempty" json:"key_path,omitempty"`
	Password    string            `yaml:"password,omitempty" json:"password,omitempty"`
	JumpHost    string            `yaml:"jump_host,omitempty" json:"jump_host,omitempty"`
	SkipBatch   bool              `yaml:"skip_batch,omitempty" json:"skip_batch,omitempty"`
	Tags        []string          `yaml:"tags,omitempty" json:"tags,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
}

// ConfigFile represents the YAML structure of the servers configuration file.
type ConfigFile struct {
	Servers []Server `yaml:"servers"`
}

// RejectLegacy1PRefs reports whether this server still holds a 1Password
// op:// reference in a credential field.
//
// Credentials are local now, so a reference left in servers.yaml can only fail
// once the connection is attempted. Callers that drive ssh(1) or sftp(1)
// directly - and therefore never reach executor.BuildClientConfig - must check
// this themselves, otherwise the literal "op://..." string is handed to the
// binary as a key path.
func (s *Server) RejectLegacy1PRefs() error {
	if s == nil {
		return nil
	}
	if err := secret.RejectLegacy1PRef("key_path", s.KeyPath, s.Name); err != nil {
		return err
	}
	return secret.RejectLegacy1PRef("password", s.Password, s.Name)
}

// Validate checks if the server definition is valid.
func (s *Server) Validate() error {
	if err := ValidateServerName(s.Name); err != nil {
		return err
	}
	if strings.TrimSpace(s.Host) == "" {
		return ErrInvalidHost
	}
	if strings.TrimSpace(s.Host) != s.Host {
		return fmt.Errorf("%w: host cannot have leading or trailing whitespace", ErrInvalidHost)
	}
	if err := rejectControlCharacters("host", s.Host); err != nil {
		return err
	}
	if s.Port < 0 || s.Port > 65535 {
		return fmt.Errorf("%w: %d is outside the valid range 1-65535", ErrInvalidPort, s.Port)
	}
	if s.Port == 0 {
		s.Port = 22
	}
	if strings.TrimSpace(s.User) == "" {
		s.User = "root"
	}
	if strings.TrimSpace(s.User) != s.User {
		return fmt.Errorf("invalid user: cannot have leading or trailing whitespace")
	}
	if err := rejectControlCharacters("user", s.User); err != nil {
		return err
	}
	if err := rejectControlCharacters("key_path", s.KeyPath); err != nil {
		return err
	}
	if err := rejectControlCharacters("description", s.Description); err != nil {
		return err
	}
	for i, tag := range s.Tags {
		if err := rejectControlCharacters(fmt.Sprintf("tags[%d]", i), tag); err != nil {
			return err
		}
	}
	for key, value := range s.Labels {
		if err := rejectControlCharacters("label key", key); err != nil {
			return err
		}
		if err := rejectControlCharacters(fmt.Sprintf("label %q", key), value); err != nil {
			return err
		}
	}
	if strings.TrimSpace(s.JumpHost) != s.JumpHost {
		return fmt.Errorf("invalid jump_host: cannot have leading or trailing whitespace")
	}
	if err := rejectControlCharacters("jump_host", s.JumpHost); err != nil {
		return err
	}
	if s.JumpHost != "" && strings.EqualFold(strings.TrimSpace(s.JumpHost), strings.TrimSpace(s.Name)) {
		return ErrSelfReferencingJumpHost
	}
	return nil
}

func rejectControlCharacters(field, value string) error {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid %s: contains ASCII control character %U", field, r)
		}
	}
	return nil
}

// Address returns the host:port string for network dialing.
func (s *Server) Address() string {
	port := s.Port
	if port <= 0 {
		port = 22
	}
	return net.JoinHostPort(s.Host, strconv.Itoa(port))
}

// FormatLabels returns the labels formatted as sorted key=val,key2=val2 string.
func (s *Server) FormatLabels() string {
	if len(s.Labels) == 0 {
		return "-"
	}

	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, s.Labels[k]))
	}
	return strings.Join(parts, ",")
}

// MatchFilter checks whether the server matches a key=value, key, value, tag, or name filter string.
func (s *Server) MatchFilter(filter string) bool {
	trimmed := strings.TrimSpace(filter)
	if trimmed == "" || strings.EqualFold(trimmed, "all") {
		return true
	}

	// 1. Check if filter is key=value
	if strings.Contains(trimmed, "=") {
		parts := strings.SplitN(trimmed, "=", 2)
		k, v := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if val, ok := s.Labels[k]; ok && strings.EqualFold(val, v) {
			return true
		}
		return false
	}

	// 2. Check matches any label key or label value
	for k, v := range s.Labels {
		if strings.EqualFold(k, trimmed) || strings.EqualFold(v, trimmed) {
			return true
		}
	}

	// 3. Check in Tags
	for _, tag := range s.Tags {
		if strings.EqualFold(tag, trimmed) {
			return true
		}
	}

	// 4. Check in Server Name
	return strings.EqualFold(s.Name, trimmed)
}

// MatchBatchFilter checks whether the server matches a filter for batch operations.
// Servers with SkipBatch=true are excluded unless includeSkipped is true.
func (s *Server) MatchBatchFilter(filter string, includeSkipped bool) bool {
	if s.SkipBatch && !includeSkipped {
		return false
	}
	return s.MatchFilter(filter)
}

var legacySSHKeys = []string{"legacy-ssh", "legacy_ssh", "legacy-rsa"}

// HasTag reports whether the server has the specified tag (case-insensitive).
func (s *Server) HasTag(tag string) bool {
	if s == nil {
		return false
	}
	trimmed := strings.TrimSpace(tag)
	if trimmed == "" {
		return false
	}
	for _, t := range s.Tags {
		if strings.EqualFold(strings.TrimSpace(t), trimmed) {
			return true
		}
	}
	return false
}

// IsLegacySSH reports whether this server requires compatibility options
// for legacy SSH daemons (e.g. ssh-rsa/ssh-dss host key negotiation).
// This is opt-in via the "legacy-ssh", "legacy_ssh", or "legacy-rsa" tag,
// or via label "legacy-ssh=true", "legacy-rsa=true", etc.
func (s *Server) IsLegacySSH() bool {
	if s == nil {
		return false
	}
	for _, k := range legacySSHKeys {
		if s.HasTag(k) {
			return true
		}
	}
	for k, v := range s.Labels {
		for _, want := range legacySSHKeys {
			if strings.EqualFold(k, want) {
				lower := strings.ToLower(strings.TrimSpace(v))
				if lower == "true" || lower == "yes" || lower == "1" {
					return true
				}
			}
		}
	}
	return false
}
