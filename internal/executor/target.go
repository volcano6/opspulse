package executor

import (
	"fmt"

	"github.com/volcano6/opspulse/internal/server"
)

// Target represents an execution destination (remote server or local machine).
type Target struct {
	Name       string
	IsLocal    bool
	Server     *server.Server
	JumpServer *server.Server
}

// NewServerTarget creates a Target representing a remote server.
func NewServerTarget(srv server.Server) Target {
	return Target{
		Name:    srv.Name,
		IsLocal: false,
		Server:  &srv,
	}
}

// NewServerTargetWithJump creates a Target representing a remote server accessible via a jump host.
func NewServerTargetWithJump(srv server.Server, jump *server.Server) Target {
	return Target{
		Name:       srv.Name,
		IsLocal:    false,
		Server:     &srv,
		JumpServer: jump,
	}
}

// WithJumpServer returns a copy of Target with JumpServer set.
func (t Target) WithJumpServer(jump *server.Server) Target {
	t.JumpServer = jump
	return t
}

// NewLocalTarget creates a Target representing the local machine.
func NewLocalTarget() Target {
	return Target{
		Name:    "local",
		IsLocal: true,
		Server:  nil,
	}
}

// ResolveTarget determines whether the target is local or a remote server from inventory.
func ResolveTarget(store *server.Store, serverName string) (Target, error) {
	if serverName == "local" || serverName == "" {
		return NewLocalTarget(), nil
	}

	if store == nil {
		return Target{}, fmt.Errorf("serverStore is nil, cannot resolve %q", serverName)
	}

	srv, err := store.Get(serverName)
	if err != nil {
		return Target{}, fmt.Errorf("server %q not found in inventory: %w", serverName, err)
	}

	return NewServerTarget(*srv), nil
}
