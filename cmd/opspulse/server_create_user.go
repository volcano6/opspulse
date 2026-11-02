package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/crypto/ssh"
)

const (
	createUserManagedMarker = "# Managed by OpsPulse; do not edit."
	createUserSudoersName   = "90-opspulse-"
	keyOnlySSHDropInName    = "00-opspulse-key-only.conf"
)

var linuxUserNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

type createUserOptions struct {
	username  string
	keyPath   string
	noCopyKey bool
	yes       bool
}

type createUserDeps struct {
	store       *server.Store
	executor    executor.Executor
	resolveKey  func(*server.Server, createUserOptions, io.Reader, io.Writer) (string, string, []byte, error)
	interactive bool
}

var serverCreateUserCmd = &cobra.Command{
	Use:   "create-user <name> <username>",
	Short: "创建 key-only sudo 用户并锁定全机 SSH 密码与 root 登录",
	Long: `在 Ubuntu/Debian 服务器上创建或更新一个仅允许 SSH 密钥登录的 sudo 用户。

命令按以下顺序执行，任何一步失败都会停在破坏性修改之前：

  1. 创建用户（已存在时复用）、加入 sudo 组，并让 authorized_keys 只保留指定公钥；
  2. 为该用户写入 NOPASSWD sudoers 规则，并验证 sudo 可用；
  3. 用新用户名和新密钥建立一次独立 SSH 连接，证明密钥登录和 sudo 都可用；
  4. 把 servers.yaml 中的服务器切换到新用户和新密钥，并清除旧明文密码；
  5. 全局禁用 SSH 密码与 keyboard-interactive 认证，只允许 publickey；
  6. 设置 PermitRootLogin no，重载 sshd，并再次验证新用户仍可登录。

未指定 --key 时优先复用服务器当前绑定的私钥；没有绑定私钥时生成 OpsPulse
专用密钥 ~/.ssh/opspulse_<name>。新用户使用 NOPASSWD sudo，因此不需要设置
可登录密码。目标用户不能是 root。authorized_keys 中除指定公钥外的旧公钥会被移除。

该操作默认要求交互确认；非交互脚本必须显式加 --yes。命令不会删除 root 账户
或私钥。SSH 被锁定后，如新用户连接异常，请从云厂商控制台恢复：

  rm -f /etc/ssh/sshd_config.d/00-opspulse-key-only.conf
  systemctl reload ssh 2>/dev/null || systemctl reload sshd

示例：
  ops server create-user hw-sh ubuntu
  ops server create-user hw-sh ubuntu --key ~/.ssh/existing_server_key
  ops server create-user hw-sh ubuntu --yes`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		keyPath, _ := cmd.Flags().GetString("key")
		noCopyKey, _ := cmd.Flags().GetBool("no-copy-key")
		yes, _ := cmd.Flags().GetBool("yes")

		opts := createUserOptions{
			username:  args[1],
			keyPath:   keyPath,
			noCopyKey: noCopyKey,
			yes:       yes,
		}
		return runServerCreateUser(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), args[0], opts, defaultCreateUserDeps(cmd))
	},
}

func defaultCreateUserDeps(cmd *cobra.Command) createUserDeps {
	store := server.NewDefaultStore()
	exec := executor.NewSSHExecutor().WithServerResolver(store.Get).WithWarnWriter(cmd.ErrOrStderr())
	if stdinIsInteractive() {
		exec = exec.WithUnknownHostKeyHandler(promptUnknownHostKey(cmd.InOrStdin(), cmd.OutOrStdout()))
	}
	return createUserDeps{
		store:       store,
		executor:    exec,
		resolveKey:  resolveCreateUserKey,
		interactive: stdinIsInteractive(),
	}
}

func runServerCreateUser(ctx context.Context, in io.Reader, out io.Writer, serverName string, opts createUserOptions, deps createUserDeps) error {
	if err := validateCreateUserName(opts.username); err != nil {
		return err
	}
	if deps.store == nil {
		return fmt.Errorf("server create-user requires a store")
	}
	if deps.executor == nil {
		return fmt.Errorf("server create-user requires an executor")
	}
	if deps.resolveKey == nil {
		return fmt.Errorf("server create-user requires a key resolver")
	}

	srv, err := deps.store.Get(serverName)
	if err != nil {
		return err
	}
	if err := srv.RejectLegacy1PRefs(); err != nil {
		return err
	}
	if err := confirmSSHLockdown(in, out, deps.interactive, opts.yes); err != nil {
		return err
	}

	storedKeyPath, _, publicKey, err := deps.resolveKey(srv, opts, in, out)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "🔐 Creating sudo user %q on %s (%s)...\n", opts.username, srv.Name, srv.Address())
	createCtx, cancelCreate := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelCreate()
	if err := executeCreateUserScript(createCtx, deps.executor, *srv, opts.username, publicKey, out); err != nil {
		return err
	}
	fmt.Fprintf(out, "✅ User %q, key, and NOPASSWD sudo configured.\n", opts.username)

	fmt.Fprintf(out, "🔎 Verifying SSH key login and sudo as %s@%s...\n", opts.username, srv.Host)
	if err := verifyCreatedUser(ctx, deps.executor, *srv, opts.username, storedKeyPath); err != nil {
		return fmt.Errorf("new user verification failed; servers.yaml was not changed and SSH was not locked down: %w", err)
	}
	fmt.Fprintf(out, "✅ Verified: %s can log in with the configured key and run sudo without an extra login credential.\n", opts.username)

	updated := *srv
	updated.User = opts.username
	updated.KeyPath = storedKeyPath
	updated.Password = ""
	if err := deps.store.Save(updated); err != nil {
		return fmt.Errorf("save user switch for server %q: %w (remote user %q is usable; SSH was not locked down)", srv.Name, err, opts.username)
	}
	fmt.Fprintf(out, "✅ servers.yaml now connects to %s as %q with %s.\n", srv.Name, opts.username, storedKeyPath)

	fmt.Fprintf(out, "🔒 Enforcing key-only SSH on %s...\n", srv.Name)
	lockdownCtx, cancelLockdown := context.WithTimeout(ctx, 45*time.Second)
	defer cancelLockdown()
	if err := executeSSHLockdownScript(lockdownCtx, deps.executor, updated, out); err != nil {
		return fmt.Errorf("new user is active and servers.yaml was switched, but locking down SSH failed: %w", err)
	}

	fmt.Fprintf(out, "🔎 Re-checking %s after the key-only SSH policy was applied...\n", opts.username)
	if err := verifyCreatedUser(ctx, deps.executor, updated, opts.username, storedKeyPath); err != nil {
		return fmt.Errorf(
			"key-only SSH and root login disablement were applied, but the post-change verification for %q failed: %w\n"+
				"recover from the provider console with:\n"+
				"  rm -f /etc/ssh/sshd_config.d/%s\n"+
				"  systemctl reload ssh 2>/dev/null || systemctl reload sshd",
			opts.username, err, keyOnlySSHDropInName,
		)
	}

	fmt.Fprintf(out, "✅ SSH is now key-only; root SSH is disabled and %s still authenticates with its key.\n", opts.username)
	fmt.Fprintf(out, "   Next: ops ssh %s\n", srv.Name)
	return nil
}

func validateCreateUserName(username string) error {
	if username == "root" {
		return fmt.Errorf("refusing to create or configure the root account; choose a non-root username such as ubuntu")
	}
	if !linuxUserNameRE.MatchString(username) {
		return fmt.Errorf("invalid Linux username %q: use 1-32 lowercase letters, digits, '_' or '-', starting with a letter or '_'", username)
	}
	return nil
}

func confirmSSHLockdown(in io.Reader, out io.Writer, interactive, yes bool) error {
	if yes {
		return nil
	}
	if !interactive {
		return fmt.Errorf("refusing to disable SSH login passwords and root login without confirmation; re-run with --yes to accept this in a non-interactive shell")
	}
	prompt := "⚠️  This will replace the new user's authorized_keys with only the selected key, verify key login and NOPASSWD sudo,\n" +
		"then globally disable SSH login passwords and root login. Existing non-root users will still\n" +
		"be able to log in only with their SSH keys. Use the provider console if the new user cannot connect.\n" +
		"Continue? [y/N]: "
	if !promptConfirm(in, out, prompt, false) {
		return fmt.Errorf("user provisioning cancelled by user")
	}
	return nil
}

func resolveCreateUserKey(srv *server.Server, opts createUserOptions, in io.Reader, out io.Writer) (string, string, []byte, error) {
	var storedPath string
	var err error

	switch {
	case strings.TrimSpace(opts.keyPath) != "":
		storedPath, _, err = ResolveAndSecureKeyPath(in, out, srv.Name, opts.keyPath, opts.noCopyKey)
		if err != nil {
			suggestedPath, _, pathErr := setupKeyPath(srv.Name)
			if pathErr == nil {
				return "", "", nil, fmt.Errorf("resolve --key %q: %w; omit --key to generate and use %s", opts.keyPath, err, suggestedPath)
			}
			return "", "", nil, fmt.Errorf("resolve --key %q: %w", opts.keyPath, err)
		}
	case srv.KeyPath != "":
		storedPath = srv.KeyPath
	default:
		var expandedPath string
		storedPath, expandedPath, err = setupKeyPath(srv.Name)
		if err != nil {
			return "", "", nil, err
		}
		if err := ensureSSHKeyPair(expandedPath, srv.Name); err != nil {
			return "", "", nil, err
		}
	}

	expandedPath := expandHome(storedPath)
	if _, err := os.Stat(expandedPath); err != nil {
		return "", "", nil, fmt.Errorf("private key %q is not available: %w", storedPath, err)
	}
	keyBytes, err := os.ReadFile(filepath.Clean(expandedPath)) // #nosec G304 -- path comes from inventory or the explicit CLI flag
	if err != nil {
		return "", "", nil, fmt.Errorf("read private key %q: %w", storedPath, err)
	}
	if _, err := ssh.ParsePrivateKey(keyBytes); err != nil {
		return "", "", nil, fmt.Errorf("private key %q must be usable without a passphrase for unattended verification: %w", storedPath, err)
	}

	pubPath := expandedPath + ".pub"
	if _, err := os.Stat(pubPath); os.IsNotExist(err) {
		if err := derivePublicKey(expandedPath); err != nil {
			return "", "", nil, err
		}
	} else if err != nil {
		return "", "", nil, fmt.Errorf("inspect public key %q: %w", pubPath, err)
	}
	pubBytes, err := os.ReadFile(filepath.Clean(pubPath)) // #nosec G304 -- companion to the resolved private key
	if err != nil {
		return "", "", nil, fmt.Errorf("read public key %q: %w", pubPath, err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(pubBytes)
	if err != nil {
		return "", "", nil, fmt.Errorf("parse public key %q: %w", pubPath, err)
	}
	return storedPath, expandedPath, ssh.MarshalAuthorizedKey(pub), nil
}

func executeCreateUserScript(ctx context.Context, exec executor.Executor, srv server.Server, username string, publicKey []byte, out io.Writer) error {
	res, err := exec.Execute(ctx, executor.NewServerTarget(srv), "create-user", createUserScript(username, publicKey), out)
	if err != nil {
		return fmt.Errorf("create user %q on %q: %w", username, srv.Name, err)
	}
	if res == nil || !res.Success {
		if res == nil {
			return fmt.Errorf("create user %q on %q failed without a result", username, srv.Name)
		}
		return fmt.Errorf("create user %q on %q failed: %v", username, srv.Name, res.Error)
	}
	return nil
}

func verifyCreatedUser(ctx context.Context, exec executor.Executor, srv server.Server, username, keyPath string) error {
	candidate := srv
	candidate.User = username
	candidate.KeyPath = keyPath
	candidate.Password = ""

	testCtx, cancelTest := context.WithTimeout(ctx, 30*time.Second)
	defer cancelTest()
	if _, _, err := exec.Test(testCtx, executor.NewServerTarget(candidate)); err != nil {
		return fmt.Errorf("SSH key login for %s@%s failed: %w", username, srv.Host, err)
	}

	verifyCtx, cancelVerify := context.WithTimeout(ctx, 30*time.Second)
	defer cancelVerify()
	var output bytes.Buffer
	res, err := exec.Execute(verifyCtx, executor.NewServerTarget(candidate), "verify-user-sudo", "sudo -n true", &output)
	if err != nil {
		return fmt.Errorf("sudo verification for %s@%s failed: %w", username, srv.Host, err)
	}
	if res == nil || !res.Success {
		return fmt.Errorf("sudo verification for %s@%s failed: %v", username, srv.Host, res)
	}
	return nil
}

func executeSSHLockdownScript(ctx context.Context, exec executor.Executor, srv server.Server, out io.Writer) error {
	res, err := exec.Execute(ctx, executor.NewServerTarget(srv), "lockdown-ssh", sshLockdownScript(), out)
	if err != nil {
		return err
	}
	if res == nil || !res.Success {
		if res == nil {
			return fmt.Errorf("SSH lockdown script failed without a result")
		}
		return fmt.Errorf("SSH lockdown script failed: %v", res.Error)
	}
	return nil
}

func createUserScript(username string, publicKey []byte) string {
	encodedKey := base64.StdEncoding.EncodeToString(bytes.TrimSpace(publicKey))
	quotedUser := shellSingleQuote(username)
	return fmt.Sprintf(`set -euo pipefail
umask 077

if [ "$(id -u)" -ne 0 ]; then
    echo "OpsPulse: root privileges are required to create the user." >&2
    exit 1
fi
if [ ! -f /etc/debian_version ]; then
    echo "OpsPulse: server create-user currently supports Ubuntu/Debian only." >&2
    exit 1
fi

USER_NAME=%s
if ! id -u "$USER_NAME" >/dev/null 2>&1; then
    useradd --create-home --shell /bin/bash --user-group "$USER_NAME"
    CREATED=1
else
    CREATED=0
fi

if ! command -v sudo >/dev/null 2>&1; then
    apt-get update -y
    DEBIAN_FRONTEND=noninteractive apt-get install -y sudo
fi

if getent group sudo >/dev/null 2>&1; then
    SUDO_GROUP=sudo
elif getent group wheel >/dev/null 2>&1; then
    SUDO_GROUP=wheel
else
    groupadd --system sudo
    SUDO_GROUP=sudo
fi
usermod -aG "$SUDO_GROUP" "$USER_NAME"

USER_HOME="$(getent passwd "$USER_NAME" | awk -F: '{print $6}')"
USER_GROUP="$(id -gn "$USER_NAME")"
if [ -z "$USER_HOME" ] || [ ! -d "$USER_HOME" ]; then
    echo "OpsPulse: home directory for $USER_NAME is missing." >&2
    exit 1
fi

install -d -m 0700 -o "$USER_NAME" -g "$USER_GROUP" "$USER_HOME/.ssh"
AUTHORIZED_KEYS="$USER_HOME/.ssh/authorized_keys"
PUBLIC_KEY="$(printf '%%s' '%s' | base64 -d)"
AUTHORIZED_KEYS_TMP="$(mktemp "$USER_HOME/.ssh/.authorized_keys.XXXXXX")"
printf '%%s\n' "$PUBLIC_KEY" > "$AUTHORIZED_KEYS_TMP"
chown "$USER_NAME:$USER_GROUP" "$AUTHORIZED_KEYS_TMP"
chmod 0600 "$AUTHORIZED_KEYS_TMP"
mv -f "$AUTHORIZED_KEYS_TMP" "$AUTHORIZED_KEYS"
chmod 0700 "$USER_HOME/.ssh"
chmod 0600 "$AUTHORIZED_KEYS"
if [ "$CREATED" = "1" ]; then
    passwd -l "$USER_NAME" >/dev/null 2>&1 || true
fi

SUDOERS_FILE="/etc/sudoers.d/%s${USER_NAME}"
if [ -f "$SUDOERS_FILE" ] && ! grep -q '^%s' "$SUDOERS_FILE"; then
    echo "OpsPulse: refusing to overwrite unmanaged sudoers file $SUDOERS_FILE" >&2
    exit 1
fi
SUDOERS_TMP="$(mktemp)"
printf '%%s\n%%s ALL=(ALL:ALL) NOPASSWD:ALL\n' '%s' "$USER_NAME" > "$SUDOERS_TMP"
chmod 0440 "$SUDOERS_TMP"
if command -v visudo >/dev/null 2>&1; then
    visudo -cf "$SUDOERS_TMP" >/dev/null
elif [ -x /usr/sbin/visudo ]; then
    /usr/sbin/visudo -cf "$SUDOERS_TMP" >/dev/null
else
    rm -f "$SUDOERS_TMP"
    echo "OpsPulse: visudo is required to validate sudoers safely." >&2
    exit 1
fi
mv "$SUDOERS_TMP" "$SUDOERS_FILE"
chmod 0440 "$SUDOERS_FILE"

if command -v runuser >/dev/null 2>&1; then
    runuser -u "$USER_NAME" -- sudo -n true
else
    su -s /bin/sh -c 'sudo -n true' "$USER_NAME"
fi

if [ "$CREATED" = "1" ]; then
    echo "OpsPulse: created user $USER_NAME."
else
    echo "OpsPulse: reused existing user $USER_NAME."
fi
echo "OpsPulse: installed key and verified NOPASSWD sudo."
`, quotedUser, encodedKey, createUserSudoersName, createUserManagedMarker, createUserManagedMarker)
}

func sshLockdownScript() string {
	return fmt.Sprintf(`set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
    echo "OpsPulse: root privileges are required to lock down SSH." >&2
    exit 1
fi

SSHD_BIN="$(command -v sshd 2>/dev/null || true)"
if [ -z "$SSHD_BIN" ] && [ -x /usr/sbin/sshd ]; then
    SSHD_BIN=/usr/sbin/sshd
fi
if [ -z "$SSHD_BIN" ]; then
    echo "OpsPulse: cannot find sshd." >&2
    exit 1
fi

DROPIN_DIR=/etc/ssh/sshd_config.d
DROPIN="$DROPIN_DIR/%s"
install -d -m 0755 "$DROPIN_DIR"
if [ -f "$DROPIN" ] && ! grep -q '^%s' "$DROPIN"; then
    echo "OpsPulse: refusing to overwrite unmanaged sshd drop-in $DROPIN" >&2
    exit 1
fi

BACKUP="$(mktemp)"
HAD_OLD_CONFIG=0
if [ -f "$DROPIN" ]; then
    cp -p "$DROPIN" "$BACKUP"
    HAD_OLD_CONFIG=1
fi

restore_config() {
    if [ "$HAD_OLD_CONFIG" = "1" ]; then
        cp -p "$BACKUP" "$DROPIN"
    else
        rm -f "$DROPIN"
    fi
}
trap 'rm -f "$BACKUP"' EXIT

printf '%%s\nPubkeyAuthentication yes\nPasswordAuthentication no\nChallengeResponseAuthentication no\nPermitRootLogin no\nAuthenticationMethods publickey\nPermitEmptyPasswords no\n' '%s' > "$DROPIN"
chmod 0644 "$DROPIN"

if ! "$SSHD_BIN" -t; then
    restore_config
    echo "OpsPulse: sshd configuration validation failed; SSH was not locked down." >&2
    exit 1
fi

SSHD_EFFECTIVE="$("$SSHD_BIN" -T 2>/dev/null)"
check_effective() {
    key="$1"
    want="$2"
    printf '%%s\n' "$SSHD_EFFECTIVE" | awk -v key="$key" -v want="$want" '$1 == key { if ($2 == want) found=1 } END { exit !found }'
}
check_kbd_disabled() {
    check_effective kbdinteractiveauthentication no ||
        check_effective challengeresponseauthentication no
}
if ! check_effective permitrootlogin no ||
   ! check_effective passwordauthentication no ||
   ! check_kbd_disabled ||
   ! check_effective authenticationmethods publickey; then
    restore_config
    printf 'OpsPulse: effective SSH settings:\n' >&2
    printf '%%s\n' "$SSHD_EFFECTIVE" |
        awk '$1 ~ /^(permitrootlogin|passwordauthentication|kbdinteractiveauthentication|challengeresponseauthentication|authenticationmethods)$/' >&2
    echo "OpsPulse: the key-only drop-in is not fully effective; SSH was not locked down." >&2
    echo "OpsPulse: OpenSSH uses the first value it reads; check /etc/ssh/sshd_config and earlier files in /etc/ssh/sshd_config.d/." >&2
    exit 1
fi

reload_sshd() {
    if command -v systemctl >/dev/null 2>&1; then
        if systemctl is-active --quiet ssh.service; then
            systemctl reload ssh.service || return 1
            return 0
        fi
        if systemctl is-active --quiet sshd.service; then
            systemctl reload sshd.service || return 1
            return 0
        fi
        if systemctl is-active --quiet ssh.socket; then
            # Socket-activated sshd reads the configuration for each new connection.
            return 0
        fi
    fi
    if command -v service >/dev/null 2>&1; then
        service ssh reload 2>/dev/null || service sshd reload || return 1
        return 0
    fi
    return 1
}

if ! reload_sshd; then
    restore_config
    reload_sshd >/dev/null 2>&1 || true
    echo "OpsPulse: could not reload sshd; SSH was not locked down." >&2
    exit 1
fi

trap - EXIT
rm -f "$BACKUP"
echo "OpsPulse: SSH now accepts public keys only; root SSH is disabled."
`, keyOnlySSHDropInName, createUserManagedMarker, createUserManagedMarker)
}

// shellSingleQuote renders a validated username as a shell literal.
func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func init() {
	serverCreateUserCmd.Flags().StringP("key", "i", "", "已存在的私钥路径（默认复用当前私钥，或生成 ~/.ssh/opspulse_<name>）")
	serverCreateUserCmd.Flags().Bool("no-copy-key", false, "私钥位于 ~/.ssh/ 之外时不提示复制到该目录")
	serverCreateUserCmd.Flags().BoolP("yes", "y", false, "无人值守确认全局禁用 SSH 密码认证与 root 登录")
	serverCreateUserCmd.ValidArgsFunction = completeCreateUserArgs
	_ = serverCreateUserCmd.RegisterFlagCompletionFunc("key", completePrivateKeyPath)
	serverCmd.AddCommand(serverCreateUserCmd)
}

func completeCreateUserArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeServerNames(cmd, args, toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}
