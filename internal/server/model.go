// Package server manages server inventory, metadata, and persistence.
package server

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	// ErrServerNotFound is returned when a requested server does not exist.
	ErrServerNotFound = errors.New("server not found")
	// ErrInvalidServerName is returned when the server name is invalid.
	ErrInvalidServerName = errors.New("server name cannot be empty")
	// ErrInvalidHost is returned when the server host is empty.
	ErrInvalidHost = errors.New("server host cannot be empty")
)

// Server represents a managed server instance.
type Server struct {
	Name        string            `yaml:"name" json:"name"`
	Host        string            `yaml:"host" json:"host"`
	Port        int               `yaml:"port" json:"port"`
	User        string            `yaml:"user" json:"user"`
	KeyPath     string            `yaml:"key_path,omitempty" json:"key_path,omitempty"`
	Password    string            `yaml:"password,omitempty" json:"password,omitempty"`
	Tags        []string          `yaml:"tags,omitempty" json:"tags,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
}

// ConfigFile represents the YAML structure of the servers configuration file.
type ConfigFile struct {
	Servers []Server `yaml:"servers"`
}

// Validate checks if the server definition is valid.
func (s *Server) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return ErrInvalidServerName
	}
	if strings.TrimSpace(s.Host) == "" {
		return ErrInvalidHost
	}
	if s.Port <= 0 || s.Port > 65535 {
		s.Port = 22
	}
	if strings.TrimSpace(s.User) == "" {
		s.User = "root"
	}
	return nil
}

// Address returns the host:port string for network dialing.
func (s *Server) Address() string {
	port := s.Port
	if port <= 0 {
		port = 22
	}
	return fmt.Sprintf("%s:%d", s.Host, port)
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
	if trimmed == "" {
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
