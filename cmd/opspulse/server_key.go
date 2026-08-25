package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
)

var serverSetupKeyCmd = &cobra.Command{
	Use:   "setup-key <name>",
	Short: "Generate and install an SSH key using the configured password",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		store := server.NewDefaultStore()
		srv, err := store.Get(args[0])
		if err != nil {
			return err
		}
		if srv.Password == "" {
			return fmt.Errorf("server %q has no configured password", srv.Name)
		}

		storedKeyPath, privateKeyPath, err := setupKeyPath(srv.Name)
		if err != nil {
			return err
		}
		if err := ensureSSHKeyPair(privateKeyPath, srv.Name); err != nil {
			return err
		}
		publicKey, err := os.ReadFile(privateKeyPath + ".pub")
		if err != nil {
			return fmt.Errorf("read generated public key: %w", err)
		}

		passwordServer := *srv
		passwordServer.KeyPath = ""
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var output bytes.Buffer
		result, err := executor.NewSSHExecutor().Execute(
			ctx,
			executor.NewServerTarget(passwordServer),
			"setup-key",
			installPublicKeyScript(publicKey),
			&output,
		)
		if err != nil {
			return fmt.Errorf("install public key on %q: %w", srv.Name, err)
		}
		if !result.Success {
			return fmt.Errorf("install public key on %q failed: %v", srv.Name, result.Error)
		}

		srv.KeyPath = storedKeyPath
		if err := store.Save(*srv); err != nil {
			return fmt.Errorf("save key path after remote installation: %w", err)
		}
		fmt.Printf("SSH key installed for %q and bound to %s. The remote password was not changed.\n", srv.Name, storedKeyPath)
		return nil
	},
}

func setupKeyPath(serverName string) (storedPath, expandedPath string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve home directory: %w", err)
	}
	var name strings.Builder
	for _, r := range serverName {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			name.WriteRune(r)
		} else {
			name.WriteByte('_')
		}
	}
	storedPath = "~/.ssh/opspulse_" + name.String()
	return storedPath, filepath.Join(home, ".ssh", "opspulse_"+name.String()), nil
}

func ensureSSHKeyPair(privateKeyPath, serverName string) error {
	if err := os.MkdirAll(filepath.Dir(privateKeyPath), 0o700); err != nil {
		return fmt.Errorf("create SSH directory: %w", err)
	}
	if _, err := os.Stat(privateKeyPath); err == nil {
		if _, pubErr := os.Stat(privateKeyPath + ".pub"); pubErr == nil {
			return nil
		}
		return derivePublicKey(privateKeyPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect private key: %w", err)
	}

	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		return fmt.Errorf("system 'ssh-keygen' not found in PATH: %w", err)
	}
	cmd := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-C", "opspulse:"+serverName, "-f", privateKeyPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("generate SSH key: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func derivePublicKey(privateKeyPath string) error {
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		return fmt.Errorf("system 'ssh-keygen' not found in PATH: %w", err)
	}
	cmd := exec.Command(keygen, "-y", "-f", privateKeyPath)
	publicKey, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("derive public key: %w", err)
	}
	if err := os.WriteFile(privateKeyPath+".pub", append(bytes.TrimSpace(publicKey), '\n'), 0o600); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}
	return nil
}

func installPublicKeyScript(publicKey []byte) string {
	encoded := base64.StdEncoding.EncodeToString(bytes.TrimSpace(publicKey))
	return fmt.Sprintf(`set -eu
umask 077
mkdir -p "$HOME/.ssh"
touch "$HOME/.ssh/authorized_keys"
key="$(printf '%%s' '%s' | base64 -d)"
grep -qxF "$key" "$HOME/.ssh/authorized_keys" || printf '%%s\n' "$key" >> "$HOME/.ssh/authorized_keys"
chmod 700 "$HOME/.ssh"
chmod 600 "$HOME/.ssh/authorized_keys"
`, encoded)
}

func init() {
	serverSetupKeyCmd.ValidArgsFunction = completeServerNames
	serverCmd.AddCommand(serverSetupKeyCmd)
}
