package server

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/shellquote"
)

// BuildJumpProxyCommand renders the ssh(1) ProxyCommand that reaches the
// target through jumpSrv.
//
// ssh(1) hands the string to /bin/sh -c, so every dynamic token must be
// single-quoted. In particular, a key path or user@host containing a space,
// quote, '$', or backtick must not be able to add arguments to the proxy ssh.
// '%h' and '%p' stay unquoted because ssh(1) substitutes them after the shell
// has tokenised the string.
func BuildJumpProxyCommand(jumpSrv *Server) string {
	if jumpSrv == nil {
		return ""
	}

	user := jumpSrv.User
	if user == "" {
		user = "root"
	}

	parts := []string{"ssh", "-W", "%h:%p"}
	if jumpSrv.IsLegacySSH() {
		parts = append(parts, "-o", "HostKeyAlgorithms=+ssh-rsa,ssh-dss", "-o", "PubkeyAcceptedKeyTypes=+ssh-rsa")
	}
	if jumpSrv.KeyPath != "" {
		keyPath := filepath.ToSlash(config.ExpandPath(jumpSrv.KeyPath))
		parts = append(parts, "-o", "IdentitiesOnly=yes", "-i", shellquote.Quote(keyPath))
	}
	if jumpSrv.Port > 0 && jumpSrv.Port != 22 {
		parts = append(parts, "-p", strconv.Itoa(jumpSrv.Port))
	}
	parts = append(parts, shellquote.Quote(fmt.Sprintf("%s@%s", user, jumpSrv.Host)))
	return strings.Join(parts, " ")
}
