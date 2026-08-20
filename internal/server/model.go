// Package server manages server inventory, metadata, and persistence.
package server

import (
	"errors"
	"fmt"
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
	Name        string   `yaml:"name"`
	Host        string   `yaml:"host"`
	Port        int      `yaml:"port"`
	User        string   `yaml:"user"`
	KeyPath     string   `yaml:"key_path,omitempty"`
	Password    string   `yaml:"password,omitempty"`
	Tags        []string `yaml:"tags,omitempty"`
	Description string   `yaml:"description,omitempty"`
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
