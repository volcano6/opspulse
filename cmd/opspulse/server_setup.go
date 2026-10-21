package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/platform"
	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
)

// 'ops server setup': rebuild the SSH working environment on a fresh WSL
// machine without taking ownership of the user's hand-written ssh config or
// silently trusting backed-up host keys.

type setupOptions struct {
	restore     bool
	writeConfig bool
	configPath  string
	trust       []string
	trustAll    bool
	connect     bool
	yes         bool
	skipProbe   bool
}

type setupDeps struct {
	store       *server.Store
	probe       func(context.Context, server.Server) ([]server.HostKey, error)
	knownHosts  string
	restore     func(context.Context, bool) error
	writeConfig func(string, []server.Server) error
	testServer  func(context.Context, server.Server) error
	interactive bool
	wsl         bool
	opBinary    string
}

type setupReport struct {
	inventory []server.Server
	checks    []server.KnownHostCheck
	missing   []string
}

var (
	serverSetupRestore     bool
	serverSetupWriteConfig bool
	serverSetupConfigPath  string
	serverSetupTrust       []string
	serverSetupTrustAll    bool
	serverSetupConnect     bool
	serverSetupYes         bool
	serverSetupSkipProbe   bool
)

var serverSetupCmd = &cobra.Command{
	Use:   "setup [server...]",
	Short: "在新 WSL 中安全重建服务器清单、私钥、SSH config 与 known_hosts",
	Long: `检查并恢复新 WSL 上的 SSH 工作环境，不覆盖手写 SSH 配置。

默认只做只读检查：服务器清单、OpsPulse 管理的私钥、known_hosts 中已有与
缺失的主机，以及每条缺失主机密钥的算法、SHA256 指纹和来源。检查不会联系
1Password、不会修改 ~/.ssh/config，也不会写入 known_hosts。

按需写入：
  --restore        复用 'ops 1p restore' 恢复 inventory 与全部凭据
  --write-config   只更新 ~/.ssh/config 中的 OpsPulse managed 区块
  --trust <name>   只登记点名服务器的主机密钥（可重复）
  --trust-all      登记本次检查中所有缺失主机（仍需交互确认；非交互需 --yes）
  --yes            无人值守确认 --restore 的明文密码写入与主机密钥导入

known_hosts 的备份内容不会被无条件信任。每台主机先展示算法与 SHA256 指纹，
用户明确确认后才写入；已记录但指纹不同的主机会被拒绝，需先用
'ssh-keygen -R <host>' 显式移除旧条目。非交互 shell 中没有 --yes 的
--trust-all 会直接失败。--yes 不会绕过硬阻止项（如 known_hosts 指纹冲突）。

示例：
  ops server setup                          # 只读体检
  ops server setup --restore                # 恢复清单与私钥
  ops server setup --write-config           # 更新 managed SSH config
  ops server setup --trust web-01 --connect # 登记一台并做连通性检查
  ops server setup --trust-all --yes        # 明确接受全部缺失主机`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := setupOptions{
			restore:     serverSetupRestore,
			writeConfig: serverSetupWriteConfig,
			configPath:  serverSetupConfigPath,
			trust:       append([]string(nil), serverSetupTrust...),
			trustAll:    serverSetupTrustAll,
			connect:     serverSetupConnect,
			yes:         serverSetupYes,
			skipProbe:   serverSetupSkipProbe,
		}
		return runServerSetup(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), opts, args, defaultSetupDeps(cmd.OutOrStdout()))
	},
}

func defaultSetupDeps(out io.Writer) setupDeps {
	store := server.NewDefaultStore()
	knownHosts, _ := server.KnownHostsPath()
	return setupDeps{
		store:      store,
		probe:      server.DefaultProbeHostKeys,
		knownHosts: knownHosts,
		restore: func(ctx context.Context, yes bool) error {
			previous := onePasswordRestoreYes
			onePasswordRestoreYes = yes
			defer func() { onePasswordRestoreYes = previous }()
			return runRestoreFromOnePassword(ctx, nil)
		},
		writeConfig: func(path string, servers []server.Server) error {
			_, _, err := server.UpdateSSHConfigFile(path, servers)
			return err
		},
		testServer: func(ctx context.Context, srv server.Server) error {
			exec := executor.NewSSHExecutor().WithServerResolver(store.Get).WithWarnWriter(out)
			_, _, err := exec.Test(ctx, executor.NewServerTarget(srv))
			return err
		},
		interactive: stdinIsInteractive(),
		wsl:         platform.IsWSL(),
		opBinary:    setupOPBinary(),
	}
}

func setupOPBinary() string {
	if path := strings.TrimSpace(os.Getenv("OPSPULSE_OP_PATH")); path != "" {
		return path
	}
	if cli := secret.Detect(); cli.Available() {
		return cli.Path
	}
	return "op (not found)"
}

func runServerSetup(ctx context.Context, in io.Reader, out io.Writer, opts setupOptions, args []string, deps setupDeps) error {
	if deps.store == nil {
		return fmt.Errorf("server setup requires a store")
	}
	if deps.probe == nil {
		deps.probe = server.DefaultProbeHostKeys
	}
	if deps.knownHosts == "" {
		path, err := server.KnownHostsPath()
		if err != nil {
			return err
		}
		deps.knownHosts = path
	}
	if deps.interactive && in == nil {
		return fmt.Errorf("interactive host-key confirmation requires stdin")
	}
	if opts.configPath != "" && !opts.writeConfig {
		return fmt.Errorf("--config requires --write-config")
	}
	if opts.yes && !opts.restore && !opts.trustAll && len(opts.trust) == 0 {
		return fmt.Errorf("--yes only applies to --restore or host-key import; add --restore, --trust <name>, or --trust-all")
	}
	if opts.trustAll && opts.skipProbe {
		return fmt.Errorf("--trust-all requires host-key probing; remove --skip-probe")
	}
	if opts.trustAll && len(opts.trust) > 0 {
		return fmt.Errorf("--trust and --trust-all contradict each other; pick one")
	}
	if len(args) > 0 && (opts.trustAll || len(opts.trust) > 0) {
		return fmt.Errorf("name servers either positionally or with --trust, not both")
	}

	if deps.wsl {
		printWSLSetupBoundary(out, deps.opBinary)
	}

	if opts.restore {
		if deps.restore == nil {
			return fmt.Errorf("restore is unavailable in this setup context")
		}
		if err := deps.restore(ctx, opts.yes); err != nil {
			return fmt.Errorf("restore the 1Password inventory and credentials: %w", err)
		}
	}

	inventory, err := deps.store.List()
	if err != nil {
		return fmt.Errorf("read the server inventory: %w", err)
	}
	if len(inventory) == 0 {
		return fmt.Errorf("the server inventory is empty; run 'ops server setup --restore' or 'ops 1p restore' first")
	}
	selected, err := setupTargets(inventory, args, opts)
	if err != nil {
		return err
	}

	report := setupReport{inventory: selected}
	fmt.Fprintf(out, "🔎 Checking %d server(s) from %s...\n", len(selected), deps.store.FilePath())
	for _, srv := range selected {
		check := checkSetupServer(ctx, deps, srv, opts.skipProbe)
		report.checks = append(report.checks, check)
		if check.Status == server.KnownHostMissing {
			report.missing = append(report.missing, srv.Name)
		}
	}
	printSetupReport(out, report)

	if opts.writeConfig {
		path := opts.configPath
		if path == "" {
			path, err = server.DefaultSSHConfigPath()
			if err != nil {
				return err
			}
		} else {
			path = expandHome(path)
		}
		if deps.writeConfig == nil {
			return fmt.Errorf("SSH config update is unavailable in this setup context")
		}
		// A positional server list and --trust narrow the setup checks. It must
		// not narrow the managed config block as a side effect: that would drop
		// every unselected OpsPulse host from the user's SSH config.
		if err := deps.writeConfig(path, inventory); err != nil {
			return fmt.Errorf("update the managed SSH config block in %s: %w", path, err)
		}
		fmt.Fprintf(out, "✅ Updated only the OpsPulse managed block (%d host(s)) in %s; hand-written Host/Include/Match entries were preserved.\n", len(inventory), path)
	}

	trustNames := append([]string(nil), opts.trust...)
	if opts.trustAll {
		trustNames = append(trustNames, report.missing...)
	}
	if len(trustNames) > 0 {
		if err := trustSetupHostKeys(in, out, deps, report.checks, trustNames, opts); err != nil {
			return err
		}
	}

	if opts.connect {
		if err := runSetupConnectivity(ctx, out, deps, selected); err != nil {
			return err
		}
	} else {
		printConnectivityHint(out, selected)
	}
	return nil
}

func setupTargets(inventory []server.Server, args []string, opts setupOptions) ([]server.Server, error) {
	if len(args) == 0 {
		if len(opts.trust) > 0 {
			// --trust names the servers to inspect as well, so a typo cannot
			// turn into a silent no-op.
			args = opts.trust
		} else {
			return inventory, nil
		}
	}
	byName := make(map[string]server.Server, len(inventory))
	for _, srv := range inventory {
		byName[srv.Name] = srv
	}
	selected := make([]server.Server, 0, len(args))
	seen := map[string]bool{}
	for _, name := range args {
		srv, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("server %q is not in the inventory; run 'ops server setup --restore' first", name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		selected = append(selected, srv)
	}
	return selected, nil
}

func checkSetupServer(ctx context.Context, deps setupDeps, srv server.Server, skipProbe bool) server.KnownHostCheck {
	if skipProbe {
		check := server.CheckKnownHost(srv, deps.knownHosts, nil)
		check.Status = server.KnownHostUnprobed
		check.Error = "probe skipped by --skip-probe"
		return check
	}
	keys, err := deps.probe(ctx, srv)
	if err != nil {
		check := server.CheckKnownHost(srv, deps.knownHosts, nil)
		check.Status = server.KnownHostProbeFail
		check.Error = err.Error()
		return check
	}
	return server.CheckKnownHost(srv, deps.knownHosts, keys)
}

func printSetupReport(out io.Writer, report setupReport) {
	fmt.Fprintln(out)
	for _, check := range report.checks {
		switch check.Status {
		case server.KnownHostPresent:
			fmt.Fprintf(out, "✅ %-16s host key already recorded in %s\n", check.Server, check.KnownHosts)
		case server.KnownHostMissing:
			fmt.Fprintf(out, "⚠️  %-16s host key missing from %s\n", check.Server, check.KnownHosts)
			for _, key := range check.Keys {
				fmt.Fprintf(out, "   %-18s %s  source: %s\n", key.Type, key.Fingerprint, key.Source)
			}
		case server.KnownHostMismatch:
			fmt.Fprintf(out, "❌ %-16s host key mismatch for %s\n", check.Server, check.Address)
			fmt.Fprintf(out, "   %s\n", check.Error)
			fmt.Fprintf(out, "   Refusing to overwrite it. Verify the fingerprint out of band, then run 'ssh-keygen -R %s -f %s'.\n", check.Host, check.KnownHosts)
		case server.KnownHostProbeFail:
			fmt.Fprintf(out, "⚠️  %-16s host key probe failed: %s\n", check.Server, check.Error)
		default:
			fmt.Fprintf(out, "⏭️  %-16s host key check skipped\n", check.Server)
		}
	}
	fmt.Fprintln(out)
	missingKeys := printManagedKeyReport(out, report.inventory)
	fmt.Fprintf(out, "known_hosts: %d present, %d missing, %d mismatch/probe issue\n",
		countStatus(report.checks, server.KnownHostPresent),
		countStatus(report.checks, server.KnownHostMissing),
		len(report.checks)-countStatus(report.checks, server.KnownHostPresent)-countStatus(report.checks, server.KnownHostMissing))
	if missingKeys > 0 {
		fmt.Fprintf(out, "❌ %d OpsPulse-managed private key(s) are missing; run 'ops server setup --restore' before connecting.\n", missingKeys)
	}
	if len(report.missing) > 0 {
		fmt.Fprintf(out, "💡 Register selectively with 'ops server setup --trust <name>' or all missing hosts with '--trust-all'.\n")
	}
}

func printManagedKeyReport(out io.Writer, servers []server.Server) int {
	missing := 0
	for _, srv := range servers {
		if srv.KeyPath == "" {
			if srv.Password != "" {
				fmt.Fprintf(out, "🔑 %-16s password authentication (no managed private key)\n", srv.Name)
			} else {
				fmt.Fprintf(out, "🔑 %-16s default SSH key/agent\n", srv.Name)
			}
			continue
		}
		if !isManagedKey(srv.KeyPath) {
			fmt.Fprintf(out, "🔑 %-16s key %s (not OpsPulse-managed; left untouched)\n", srv.Name, srv.KeyPath)
			continue
		}
		path := expandHome(srv.KeyPath)
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(out, "❌ %-16s managed private key missing: %s (run 'ops server setup --restore')\n", srv.Name, srv.KeyPath)
			missing++
			continue
		}
		mode := info.Mode().Perm()
		if runtime.GOOS != "windows" && mode&0o077 != 0 {
			fmt.Fprintf(out, "⚠️  %-16s managed private key %s has mode %04o, want 0600\n", srv.Name, srv.KeyPath, mode)
			continue
		}
		fmt.Fprintf(out, "✅ %-16s managed private key present: %s (mode %04o)\n", srv.Name, srv.KeyPath, mode)
	}
	return missing
}

func countStatus(checks []server.KnownHostCheck, status server.KnownHostStatus) int {
	count := 0
	for _, check := range checks {
		if check.Status == status {
			count++
		}
	}
	return count
}

func trustSetupHostKeys(in io.Reader, out io.Writer, deps setupDeps, checks []server.KnownHostCheck, names []string, opts setupOptions) error {
	byName := make(map[string]server.KnownHostCheck, len(checks))
	for _, check := range checks {
		byName[check.Server] = check
	}
	var selected []string
	for _, name := range names {
		check, ok := byName[name]
		if !ok {
			return fmt.Errorf("server %q was not checked in this run", name)
		}
		switch check.Status {
		case server.KnownHostPresent:
			fmt.Fprintf(out, "✅ %s: host key already recorded; nothing to import.\n", name)
		case server.KnownHostMissing:
			selected = append(selected, name)
		case server.KnownHostMismatch:
			return fmt.Errorf("refusing to trust %s: known_hosts records a different key; verify it and remove the old entry with 'ssh-keygen -R %s -f %s'", name, check.Host, check.KnownHosts)
		default:
			return fmt.Errorf("cannot trust %s: host key was not probed successfully (%s)", name, check.Error)
		}
	}
	if len(selected) == 0 {
		return nil
	}

	interactive := deps.interactive
	if !opts.yes && !interactive {
		return fmt.Errorf("refusing to import %d host key(s) without confirmation; re-run with --yes to accept this in a non-interactive shell", len(selected))
	}
	if !opts.yes {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "The following fingerprints will be trusted for these hosts. Verify them against a trusted channel before accepting.")
		for _, name := range selected {
			check := byName[name]
			for _, key := range check.Keys {
				fmt.Fprintf(out, "  %s %s %s (source: %s)\n", name, key.Type, key.Fingerprint, key.Source)
			}
		}
		if !promptConfirm(in, out, fmt.Sprintf("Import %d host key(s) into %s? [y/N]: ", countSelectedKeys(byName, selected), deps.knownHosts), false) {
			return fmt.Errorf("host key import cancelled by user")
		}
	}

	imported := 0
	for _, name := range selected {
		check := byName[name]
		srv, err := deps.store.Get(name)
		if err != nil {
			return err
		}
		fingerprints := make([]string, 0, len(check.Keys))
		for _, key := range check.Keys {
			fingerprints = append(fingerprints, key.Fingerprint)
		}
		n, err := server.AppendKnownHosts(deps.knownHosts, *srv, check.Keys, fingerprints)
		if err != nil {
			return err
		}
		imported += n
		fmt.Fprintf(out, "✅ %s: %d host key(s) added to %s.\n", name, n, deps.knownHosts)
	}
	if imported == 0 {
		fmt.Fprintln(out, "No new host key needed to be written.")
	}
	return nil
}

func countSelectedKeys(checks map[string]server.KnownHostCheck, names []string) int {
	count := 0
	for _, name := range names {
		count += len(checks[name].Keys)
	}
	return count
}

func runSetupConnectivity(ctx context.Context, out io.Writer, deps setupDeps, servers []server.Server) error {
	if deps.testServer == nil {
		return fmt.Errorf("connectivity test is unavailable in this setup context")
	}
	failed := 0
	for _, srv := range servers {
		if err := deps.testServer(ctx, srv); err != nil {
			failed++
			fmt.Fprintf(out, "❌ %s: %v\n", srv.Name, err)
			continue
		}
		fmt.Fprintf(out, "✅ %s: SSH connection succeeded\n", srv.Name)
	}
	if failed > 0 {
		return fmt.Errorf("%d server(s) failed the connectivity check", failed)
	}
	return nil
}

func printConnectivityHint(out io.Writer, servers []server.Server) {
	names := make([]string, 0, len(servers))
	for _, srv := range servers {
		names = append(names, srv.Name)
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	fmt.Fprintf(out, "\n💡 Verify connectivity with: ops doctor -f all   # or: ops server test %s\n", names[0])
}

func printWSLSetupBoundary(out io.Writer, opBinary string) {
	fmt.Fprintln(out, "🪟 WSL boundary:")
	fmt.Fprintf(out, "   - 1Password CLI: %s (WSL should use Windows op.exe; a Linux op cannot reach the Desktop App)\n", opBinary)
	fmt.Fprintln(out, "   - Private keys live in this WSL home (~/.ssh). GUI clients on Windows need the existing mirror under the Windows user profile's .ssh/opspulse directory, not a copied path in this distro.")
	fmt.Fprintln(out, "   - Running 'wsl --shutdown' or switching distros changes which ~/.ssh and known_hosts are active.")
}

func init() {
	serverSetupCmd.Flags().BoolVar(&serverSetupRestore, "restore", false, "复用 'ops 1p restore' 恢复服务器清单与全部凭据")
	serverSetupCmd.Flags().BoolVar(&serverSetupWriteConfig, "write-config", false, "只更新 ~/.ssh/config 中的 OpsPulse managed 区块")
	serverSetupCmd.Flags().StringVar(&serverSetupConfigPath, "config", "", "SSH config 路径（默认 ~/.ssh/config；需配合 --write-config）")
	serverSetupCmd.Flags().StringArrayVar(&serverSetupTrust, "trust", nil, "登记指定服务器的主机密钥（可重复）")
	serverSetupCmd.Flags().BoolVar(&serverSetupTrustAll, "trust-all", false, "登记本次检查中所有缺失的主机密钥")
	serverSetupCmd.Flags().BoolVar(&serverSetupConnect, "connect", false, "setup 结束后逐台验证 SSH 连通性")
	serverSetupCmd.Flags().BoolVarP(&serverSetupYes, "yes", "y", false, "无人值守确认 --restore 的明文密码写入与主机密钥导入")
	serverSetupCmd.Flags().BoolVar(&serverSetupSkipProbe, "skip-probe", false, "跳过 ssh-keyscan 探测（只检查清单与私钥）")
	serverSetupCmd.ValidArgsFunction = completeServerNames
	serverCmd.AddCommand(serverSetupCmd)
}
