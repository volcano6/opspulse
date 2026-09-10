package secret

import (
	"os"
	"testing"
)

func TestTry1PAgentSocket(t *testing.T) {
	// Temporarily unset SSH_AUTH_SOCK to test behavior when agent is not present
	origSock := os.Getenv("SSH_AUTH_SOCK")
	_ = os.Unsetenv("SSH_AUTH_SOCK")
	defer func() {
		if origSock != "" {
			_ = os.Setenv("SSH_AUTH_SOCK", origSock)
		}
	}()

	t.Log("Testing Try1PAgentSocket without SSH_AUTH_SOCK...")

	// Without SSH_AUTH_SOCK and assuming ~/.1password/agent.sock is not a valid
	// socket in the CI runner, it should gracefully return nil.
	conn := Try1PAgentSocket()
	if conn != nil {
		_ = conn.Close()
		// In a local dev environment with 1P running, this might return a valid connection.
		// That's fine, we just want to ensure it doesn't panic.
	}
}
