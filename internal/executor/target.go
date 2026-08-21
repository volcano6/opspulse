package executor

import "github.com/volcano6/opspulse/internal/server"

// Target represents an execution destination (remote server or local machine).
type Target struct {
	Name    string
	IsLocal bool
	Server  *server.Server
}

// NewServerTarget creates a Target representing a remote server.
func NewServerTarget(srv server.Server) Target {
	return Target{
		Name:    srv.Name,
		IsLocal: false,
		Server:  &srv,
	}
}

// NewLocalTarget creates a Target representing the local machine.
func NewLocalTarget() Target {
	return Target{
		Name:    "local",
		IsLocal: true,
		Server:  nil,
	}
}
