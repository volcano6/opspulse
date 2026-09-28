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

var serverSetupKeyRemovePassword bool

var serverSetupKeyCmd = &cobra.Command{
	Use:   "setup-key <name>",
	Short: "用已配置的密码生成并安装 SSH 密钥",
	Long: `生成 SSH 密钥对，用已保存的密码把公钥安装到远端主机，并把该服务器绑定到本地私钥。

远端密码不会被修改：这里只是在其之上增加密钥登录，
因此原有密码仍可作为后备方式继续使用。

加上 --remove-password 时，OpsPulse 会先证明新密钥能独立完成认证（刻意不提供密码），
之后才删除 servers.yaml 中的明文密码。这样，实际回退到密码认证的验证
就不可能被误判为密钥可用。

示例：
  ops server setup-key web-01                     # 生成并安装专用密钥
  ops server setup-key web-01 --remove-password   # 密钥可用后同时清除明文密码`,
	Args: cobra.ExactArgs(1),
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
		result, err := executor.NewSSHExecutor().WithServerResolver(store.Get).WithWarnWriter(os.Stderr).Execute(
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

		if serverSetupKeyRemovePassword {
			return removePasswordAfterKeyVerification(store, srv, storedKeyPath)
		}
		return nil
	},
}

// removePasswordAfterKeyVerification proves the new key can authenticate on its
// own before the plaintext password is dropped.
//
// The probe uses a copy of the server with the password forced empty, so a
// connection that actually fell back to password authentication cannot be
// mistaken for a working key. Only after that succeeds is the password deleted
// from servers.yaml; on failure it is kept, because locking the user out of a
// host is far worse than leaving a secret in a 0600 file.
func removePasswordAfterKeyVerification(store *server.Store, srv *server.Server, storedKeyPath string) error {
	keySrv := *srv
	keySrv.KeyPath = storedKeyPath
	keySrv.Password = ""

	fmt.Printf("🔐 Verifying that %q accepts the new key on its own...\n", srv.Name)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	exec := executor.NewSSHExecutor().WithServerResolver(store.Get).WithWarnWriter(os.Stderr)
	if _, _, err := exec.Test(ctx, executor.NewServerTarget(keySrv)); err != nil {
		return fmt.Errorf("pure key authentication for %q failed, so the password was kept: %w\n\ncheck that the remote sshd_config allows PubkeyAuthentication", srv.Name, err)
	}

	srv.Password = ""
	if err := store.Save(*srv); err != nil {
		return fmt.Errorf("remove the password from servers.yaml: %w", err)
	}
	fmt.Printf("✅ Key authentication verified; the plaintext password was removed from servers.yaml.\n")
	return nil
}

func setupKeyPath(serverName string) (storedPath, expandedPath string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve home directory: %w", err)
	}
	name := sanitiseName(serverName)
	storedPath = "~/.ssh/opspulse_" + name
	return storedPath, filepath.Join(home, ".ssh", "opspulse_"+name), nil
}

// sanitiseName reduces a name to the characters that are safe in a filename and
// in a 1Password item title. Everything else becomes an underscore rather than
// being dropped, so two different names cannot collapse into the same one.
func sanitiseName(name string) string {
	var out strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			out.WriteRune(r)
		} else {
			out.WriteByte('_')
		}
	}
	return out.String()
}

// machineName identifies this machine in the vault, as the suffix of its backup
// item's title.
//
// The hostname is the only stable thing a machine knows about itself, and it is
// sanitised because it ends up in an item title and in every error message about
// the backup. A machine that cannot report a hostname still has to be able to
// back up, so the empty case gets a name rather than an error - the worst outcome
// is an item titled opspulse_inventory_unknown, which is still restorable.
func machineName() string {
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	name := sanitiseName(strings.TrimSpace(host))
	if name == "" {
		return "unknown"
	}
	return name
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
	serverSetupKeyCmd.Flags().BoolVar(&serverSetupKeyRemovePassword, "remove-password", false, "验证密钥可独立认证后，从 servers.yaml 中删除明文密码")
	serverSetupKeyCmd.ValidArgsFunction = completeServerNames
	serverCmd.AddCommand(serverSetupKeyCmd)
}
