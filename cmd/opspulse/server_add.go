package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/term"
)

var addCmd = &cobra.Command{
	Use:   "add <name> [target]",
	Short: "新增或更新清单中的服务器",
	Long: `新增或更新 servers.yaml 中的服务器。

目标可用位置参数 [user@]host[:port] 指定，也可通过 flag 指定。
默认用户为 root，默认 SSH 端口为 22。

新增时服务器名会被规范化：下划线改写为中划线，"web_1" 保存为 "web-1"，
避免同一台机器以两个肉眼难分的名字在清单里出现两次。已存在的条目不会被改名。

示例：
  # 静默输入密码添加服务器，并自动注入公钥
  ops add vps-1 1.2.3.4

  # 指定自定义用户与端口
  ops add prod ubuntu@1.2.3.4:2222

  # 指定私钥
  ops add backup 1.2.3.4 -i ~/.ssh/id_ed25519

  # 使用 flag 添加
  ops add node-1 --host 10.0.0.1 --labels env=prod,provider=racknerd`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runServerAdd,
}

var serverAddCmd = &cobra.Command{
	Use:   "add <name> [target]",
	Short: "新增或更新清单中的服务器",
	Long:  addCmd.Long,
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runServerAdd,
}

func setupAddFlags(cmd *cobra.Command) {
	cmd.Flags().String("host", "", "服务器 IP 或主机名")
	cmd.Flags().IntP("port", "p", 22, "SSH 端口")
	cmd.Flags().StringP("user", "u", "root", "SSH 用户名")
	cmd.Flags().StringP("identity", "i", "", "私钥文件路径")
	cmd.Flags().StringP("key", "k", "", "私钥文件路径（-i 的别名）")
	cmd.Flags().StringP("jump-host", "J", "", "清单中的跳板机服务器名（Bastion / Jump Host）")
	cmd.Flags().Bool("no-copy-key", false, "私钥位于 ~/.ssh/ 之外时不提示复制到该目录")
	cmd.Flags().Bool("skip-test", false, "添加服务器时跳过 SSH 连通性测试")
	cmd.Flags().String("password", "", "SSH 密码（可选；未指定私钥且未提供密码时交互式输入）。注意：在此传入的密码会以明文记录在 Shell 历史中，建议改用交互式提示或 ops server setup-key")
	cmd.Flags().String("tags", "", "逗号分隔的标签（如 prod,web）")
	cmd.Flags().StringP("labels", "l", "", "逗号分隔的 key=value Label（如 provider=oracle,region=sg）")
	cmd.Flags().StringP("desc", "d", "", "服务器描述")
	cmd.Flags().Bool("skip-batch", false, "将该服务器排除在隐式批量操作之外（如 ops exec -f all、ops doctor）")
	_ = cmd.RegisterFlagCompletionFunc("identity", completePrivateKeyPath)
	_ = cmd.RegisterFlagCompletionFunc("key", completePrivateKeyPath)
	_ = cmd.RegisterFlagCompletionFunc("jump-host", completeServerNames)
}

func parseTarget(target string) (user, host string, port int, err error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return "", "", 0, nil
	}

	hostPart := trimmed
	if atIdx := strings.Index(trimmed, "@"); atIdx != -1 {
		user = strings.TrimSpace(trimmed[:atIdx])
		hostPart = strings.TrimSpace(trimmed[atIdx+1:])
	}

	if hostPart == "" {
		return "", "", 0, fmt.Errorf("target host cannot be empty")
	}

	// Handle bracketed IPv6: [2001:db8::1]:22 or [2001:db8::1]
	if strings.HasPrefix(hostPart, "[") {
		closeBracket := strings.LastIndex(hostPart, "]")
		if closeBracket == -1 {
			return "", "", 0, fmt.Errorf("unmatched '[' in target: %s", target)
		}
		host = hostPart[1:closeBracket]
		rest := hostPart[closeBracket+1:]
		if strings.HasPrefix(rest, ":") {
			p, err := strconv.Atoi(rest[1:])
			if err != nil || p <= 0 || p > 65535 {
				return "", "", 0, fmt.Errorf("invalid port in target: %s", rest[1:])
			}
			port = p
		}
		return user, host, port, nil
	}

	// Handle host:port
	if strings.Contains(hostPart, ":") {
		if strings.Count(hostPart, ":") == 1 {
			h, p, err := net.SplitHostPort(hostPart)
			if err != nil {
				return "", "", 0, fmt.Errorf("invalid host:port %q: %w", hostPart, err)
			}
			portNum, err := strconv.Atoi(p)
			if err != nil || portNum <= 0 || portNum > 65535 {
				return "", "", 0, fmt.Errorf("invalid port in target: %s", p)
			}
			return user, h, portNum, nil
		}
		// Multiple colons and no brackets: bare IPv6 address without port
		return user, hostPart, 0, nil
	}

	return user, hostPart, 0, nil
}

func findDefaultPublicKey() (pubPath, privPath string, exists bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", false
	}
	candidates := []string{
		"id_ed25519",
		"id_rsa",
		"id_ecdsa",
	}
	for _, name := range candidates {
		pub := filepath.Join(home, ".ssh", name+".pub")
		priv := filepath.Join(home, ".ssh", name)
		if info, err := os.Stat(pub); err == nil && !info.IsDir() {
			if privInfo, privErr := os.Stat(priv); privErr == nil && !privInfo.IsDir() {
				return "~/.ssh/" + name + ".pub", "~/.ssh/" + name, true
			}
		}
	}
	return "", "", false
}

func promptPassword(in io.Reader, out io.Writer, user, host string) (string, error) {
	if out != nil {
		_, _ = fmt.Fprintf(out, "🔑 Enter SSH password for %s@%s (press Enter if using default SSH key): ", user, host)
	}

	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		passBytes, err := term.ReadPassword(int(f.Fd()))
		if out != nil {
			_, _ = fmt.Fprintln(out)
		}
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return strings.TrimSpace(string(passBytes)), nil
	}

	scanner := bufio.NewScanner(in)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text()), nil
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return "", nil
}

func promptConfirm(in io.Reader, out io.Writer, prompt string, defaultYes bool) bool {
	if out != nil {
		_, _ = fmt.Fprint(out, prompt)
	}
	if in == nil {
		return false
	}

	scanner := bufio.NewScanner(in)
	if scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			return defaultYes
		}
		if strings.EqualFold(text, "y") || strings.EqualFold(text, "yes") {
			return true
		}
		if strings.EqualFold(text, "n") || strings.EqualFold(text, "no") {
			return false
		}
	}
	return false
}

func handlePublicKeyInjection(in io.Reader, out io.Writer, srv *server.Server, password string) {
	pubPath, privPath, exists := findDefaultPublicKey()
	var targetPubPath, targetPrivPath string
	var isNewKey bool

	if exists {
		targetPubPath = pubPath
		targetPrivPath = privPath
	} else {
		storedPath, _, err := setupKeyPath(srv.Name)
		if err == nil {
			targetPrivPath = storedPath
			targetPubPath = storedPath + ".pub"
			isNewKey = true
		}
	}

	if targetPubPath == "" {
		return
	}

	promptMsg := fmt.Sprintf("? 是否将本地公钥 (%s) 注入该 VPS 实现永久免密直连？[Y/n]: ", targetPubPath)
	if isNewKey {
		promptMsg = fmt.Sprintf("? 本地未找到通用公钥，是否自动生成专用密钥对 (%s) 并注入该 VPS 实现永久免密直连？[Y/n]: ", targetPrivPath)
	}

	if !promptConfirm(in, out, promptMsg, true) {
		if out != nil {
			_, _ = fmt.Fprintf(out, "ℹ️ 已跳过公钥注入，保留密码认证。\n")
		}
		return
	}

	expandedPriv := expandHome(targetPrivPath)
	if isNewKey {
		if err := ensureSSHKeyPair(expandedPriv, srv.Name); err != nil {
			if out != nil {
				_, _ = fmt.Fprintf(out, "⚠️ 生成密钥失败: %v，保留密码认证。\n", err)
			}
			return
		}
	}

	expandedPub := expandHome(targetPubPath)
	pubBytes, err := os.ReadFile(filepath.Clean(expandedPub)) // #nosec G304 -- reading authorized public key file
	if err != nil {
		if out != nil {
			_, _ = fmt.Fprintf(out, "⚠️ 读取公钥 %s 失败: %v，保留密码认证。\n", targetPubPath, err)
		}
		return
	}

	if out != nil {
		_, _ = fmt.Fprintf(out, "--> 正在将公钥注入到 %s (%s)...\n", srv.Name, srv.Address())
	}

	passwordServer := *srv
	passwordServer.KeyPath = ""
	passwordServer.Password = password

	store := server.NewDefaultStore()
	exec := executor.NewSSHExecutor().WithServerResolver(store.Get).WithWarnWriter(os.Stderr)
	injectCtx, injectCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer injectCancel()

	var outBuf bytes.Buffer
	injectRes, err := exec.Execute(
		injectCtx,
		executor.NewServerTarget(passwordServer),
		"inject-key",
		installPublicKeyScript(pubBytes),
		&outBuf,
	)
	if err != nil || !injectRes.Success {
		var errDetail string
		if err != nil {
			errDetail = err.Error()
		} else {
			errDetail = fmt.Sprintf("%v", injectRes.Error)
		}
		if out != nil {
			_, _ = fmt.Fprintf(out, "⚠️ 注入公钥失败: %s，保留密码认证。\n", errDetail)
		}
		return
	}

	if out != nil {
		_, _ = fmt.Fprintf(out, "--> 验证密钥免密直连...\n")
	}
	keySrv := *srv
	keySrv.KeyPath = targetPrivPath
	keySrv.Password = ""
	keyCtx, keyCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer keyCancel()

	_, _, keyErr := exec.Test(keyCtx, executor.NewServerTarget(keySrv))
	if keyErr != nil {
		if out != nil {
			_, _ = fmt.Fprintf(out, "⚠️ 密钥连接验证失败: %v，保留密码认证。\n", keyErr)
		}
		return
	}

	srv.KeyPath = targetPrivPath
	srv.Password = "" // Secure: do not keep plaintext password once key auth is verified
	if out != nil {
		_, _ = fmt.Fprintf(out, "✅ 公钥已成功注入并验证！本地已自动升级为密钥免密直连 (%s)\n", targetPrivPath)
	}
}

// underscoreTwin reports an existing server whose name differs from name only by
// underscores, i.e. one that NormalizeServerName would collapse into name.
func underscoreTwin(store *server.Store, name string) (string, bool) {
	servers, err := store.List()
	if err != nil {
		return "", false
	}
	for _, s := range servers {
		if s.Name != name && server.NormalizeServerName(s.Name) == name {
			return s.Name, true
		}
	}
	return "", false
}

func runServerAdd(cmd *cobra.Command, args []string) error {
	name := strings.TrimSpace(args[0])
	if name == "" {
		return fmt.Errorf("server name cannot be empty")
	}

	var targetUser, targetHost string
	var targetPort int
	if len(args) >= 2 {
		u, h, p, err := parseTarget(args[1])
		if err != nil {
			return fmt.Errorf("invalid target %q: %w", args[1], err)
		}
		targetUser = u
		targetHost = h
		targetPort = p
	}

	host, _ := cmd.Flags().GetString("host")
	if host == "" {
		host = targetHost
	}
	if host == "" {
		return fmt.Errorf("host is required: specify target argument (e.g. 'ops add %s 1.2.3.4') or use --host", name)
	}

	user, _ := cmd.Flags().GetString("user")
	if targetUser != "" && (!cmd.Flags().Changed("user") || user == "root") {
		user = targetUser
	}
	if user == "" {
		user = "root"
	}

	port, _ := cmd.Flags().GetInt("port")
	if targetPort > 0 && (!cmd.Flags().Changed("port") || port == 22) {
		port = targetPort
	}
	if port < 0 || port > 65535 {
		return fmt.Errorf("%w: %d is outside the valid range 1-65535", server.ErrInvalidPort, port)
	}
	if port == 0 {
		port = 22
	}

	identity, _ := cmd.Flags().GetString("identity")
	key, _ := cmd.Flags().GetString("key")
	// --identity and --key are two names for one setting; accepting both with
	// different values would silently pick one of them.
	if cmd.Flags().Changed("identity") && cmd.Flags().Changed("key") && identity != key {
		return fmt.Errorf("cannot use both --identity and --key with different values")
	}
	if identity == "" {
		identity = key
	}

	password, _ := cmd.Flags().GetString("password")
	noCopyKey, _ := cmd.Flags().GetBool("no-copy-key")
	skipTest, _ := cmd.Flags().GetBool("skip-test")
	tagsStr, _ := cmd.Flags().GetString("tags")
	labelsStr, _ := cmd.Flags().GetString("labels")
	desc, _ := cmd.Flags().GetString("desc")
	jumpHost, _ := cmd.Flags().GetString("jump-host")
	jumpHost = strings.TrimSpace(jumpHost)

	store := server.NewDefaultStore()

	// Underscores and hyphens are hard to tell apart in many fonts and both are legal
	// in ValidateServerName, so "web_1" next to an existing "web-1" would leave two
	// inventory entries for one machine. Canonicalize the name being created.
	if normalized := server.NormalizeServerName(name); normalized != name {
		if _, err := store.Get(normalized); err == nil {
			return fmt.Errorf("server %q already exists and %q normalizes to it; run 'ops add %s ...' to update that server, or pick another name", normalized, name, normalized)
		} else if !errors.Is(err, server.ErrServerNotFound) {
			return err
		}
		_, _ = fmt.Fprintf(os.Stdout, "ℹ️  Server name %q contains underscores; using %q instead.\n", name, normalized)
		name = normalized
	} else if twin, ok := underscoreTwin(store, name); ok {
		_, _ = fmt.Fprintf(os.Stdout, "⚠️  Server %q differs from the existing %q only by underscores; if they are the same machine, remove one with 'ops server remove %s'.\n", name, twin, twin)
	}

	if jumpHost != "" {
		if strings.EqualFold(jumpHost, name) {
			return server.ErrSelfReferencingJumpHost
		}
		if _, err := store.Get(jumpHost); err != nil {
			return fmt.Errorf("jump host %q not found in inventory: %w", jumpHost, err)
		}
	}

	var tags []string
	if tagsStr != "" {
		for _, t := range strings.Split(tagsStr, ",") {
			if trimmed := strings.TrimSpace(t); trimmed != "" {
				tags = append(tags, trimmed)
			}
		}
	}

	labels := make(map[string]string)
	if labelsStr != "" {
		for _, pair := range strings.Split(labelsStr, ",") {
			trimmed := strings.TrimSpace(pair)
			if trimmed == "" {
				continue
			}
			if strings.Contains(trimmed, "=") {
				kv := strings.SplitN(trimmed, "=", 2)
				labels[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
			} else {
				labels[trimmed] = "true"
			}
		}
	}

	var finalKeyPath string
	var keyWasCopied bool
	if identity != "" {
		securedKey, copied, err := ResolveAndSecureKeyPath(os.Stdin, os.Stdout, name, identity, noCopyKey)
		if err != nil {
			return err
		}
		finalKeyPath = securedKey
		keyWasCopied = copied
	}

	usedPassword := false
	if finalKeyPath == "" && password == "" && !skipTest {
		pwd, err := promptPassword(os.Stdin, os.Stdout, user, host)
		if err != nil {
			return err
		}
		password = pwd
		if password != "" {
			usedPassword = true
		}
	} else if finalKeyPath == "" && password != "" {
		usedPassword = true
	}

	skipBatchFlag, _ := cmd.Flags().GetBool("skip-batch")
	skipBatch := false
	if cmd.Flags().Changed("skip-batch") {
		skipBatch = skipBatchFlag
	} else if existingSrv, err := store.Get(name); err == nil {
		skipBatch = existingSrv.SkipBatch
	}

	srv := server.Server{
		Name:        name,
		Host:        host,
		Port:        port,
		User:        user,
		KeyPath:     finalKeyPath,
		Password:    password,
		JumpHost:    jumpHost,
		SkipBatch:   skipBatch,
		Tags:        tags,
		Labels:      labels,
		Description: desc,
	}

	if !skipTest {
		if srv.JumpHost != "" {
			_, _ = fmt.Fprintf(os.Stdout, "--> Verifying SSH connection to %s (%s) via jump host %s...\n", srv.Name, srv.Address(), srv.JumpHost)
		} else {
			_, _ = fmt.Fprintf(os.Stdout, "--> Verifying SSH connection to %s (%s)...\n", srv.Name, srv.Address())
		}
		exec := executor.NewSSHExecutor().WithServerResolver(store.Get).WithWarnWriter(os.Stderr)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		rtt, banner, err := exec.Test(ctx, executor.NewServerTarget(srv))
		if err != nil {
			if keyWasCopied {
				cleanupManagedKey(finalKeyPath)
				_, _ = fmt.Fprintf(os.Stdout, "🧹 Cleaned up copied key: %s\n", finalKeyPath)
			}
			if finalKeyPath == "" && password == "" {
				return fmt.Errorf("❌ Connection test failed: %w (no private key specified and password was empty; use -i <key> or enter password, or use --skip-test to add anyway)", err)
			}
			return fmt.Errorf("❌ Connection test failed: %w (server was not added; use --skip-test to add anyway)", err)
		}
		_, _ = fmt.Fprintf(os.Stdout, "✅ Connection verified! (Latency: %.2f ms, %s)\n", float64(rtt.Microseconds())/1000.0, banner)

		if usedPassword {
			handlePublicKeyInjection(os.Stdin, os.Stdout, &srv, password)
		}
	}

	if err := store.Save(srv); err != nil {
		if keyWasCopied {
			cleanupManagedKey(finalKeyPath)
		}
		return fmt.Errorf("failed to save server: %w", err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "✅ Server %q (%s) saved successfully to %s\n", srv.Name, srv.Address(), store.FilePath())

	if srv.Password != "" && !secret.Is1PRef(srv.Password) {
		_, _ = fmt.Fprintf(os.Stdout, "\n⚠️  [Security Warning] Plaintext password is stored in %s.\n", store.FilePath())
		_, _ = fmt.Fprintf(os.Stdout, "   Prefer key-based login, then drop the password once it is verified:\n")
		_, _ = fmt.Fprintf(os.Stdout, "   Run: ops server setup-key %s --remove-password\n", srv.Name)
	}
	return nil
}

func init() {
	setupAddFlags(addCmd)
	setupAddFlags(serverAddCmd)
	rootCmd.AddCommand(addCmd)
	serverCmd.AddCommand(serverAddCmd)
}
