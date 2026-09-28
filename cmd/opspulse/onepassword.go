package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/secret"
)

// The 'ops 1p' command surface: the subcommands, their flags, and the vault and
// account resolution that backup, restore and status all share.

// Flags shared by 'ops 1p backup' and 'ops 1p restore'. The two commands never
// run together, and both mean the same thing by every one of these, so sharing
// them keeps the two flag surfaces from drifting apart.
var (
	onePasswordVault        string
	onePasswordAccount      string
	onePasswordPreferLocal  bool
	onePasswordPreferRemote bool
)

// itemIndex memoises a vault's item titles for the duration of one command.
//
// `op item list` is a full round trip through the Desktop App, and a batch used
// to pay it once per credential. A single snapshot is enough because OpsPulse
// only ever looks up the items it creates itself, whose titles are unique per
// server, so nothing can be created underneath the snapshot mid-run.
type itemIndex struct {
	mu      sync.Mutex
	cli     secret.CLI
	vault   string
	loaded  bool
	byTitle map[string]string
}

func newItemIndex(cli secret.CLI, vault string) *itemIndex {
	return &itemIndex{cli: cli, vault: vault, byTitle: make(map[string]string)}
}

// load fetches the vault's item list once. The first caller pays for the round
// trip; every later caller observes the same snapshot.
func (ix *itemIndex) load(ctx context.Context) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	if ix.loaded {
		return nil
	}
	out, err := ix.cli.Run(ctx, "item", "list", "--vault", ix.vault, "--format", "json")
	if err != nil {
		return fmt.Errorf("%s\n\nlist items in 1Password vault %q: %w", onePasswordFailureHint(ix.cli, err), ix.vault, err)
	}
	var items []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		return fmt.Errorf("parse 1Password item list: %w", err)
	}
	for _, item := range items {
		ix.byTitle[item.Title] = item.ID
	}
	ix.loaded = true
	return nil
}

// id returns the item ID for title, or "" when the vault holds no such item.
func (ix *itemIndex) id(ctx context.Context, title string) (string, error) {
	if err := ix.load(ctx); err != nil {
		return "", err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.byTitle[title], nil
}

// titles returns the set of item titles in the vault, sharing the same single
// listing as id. Restore needs the whole set to match servers by item name, and
// paying for a second `op item list` to get it would be a second Desktop App
// authorisation on a platform that has no cache to absorb it.
func (ix *itemIndex) titles(ctx context.Context) (map[string]struct{}, error) {
	if err := ix.load(ctx); err != nil {
		return nil, err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	set := make(map[string]struct{}, len(ix.byTitle))
	for title := range ix.byTitle {
		set[title] = struct{}{}
	}
	return set, nil
}

// Flags that only make sense while writing values back onto this machine.
var (
	onePasswordRestoreYes   bool
	onePasswordRestoreForce bool
)

// onePasswordFilter narrows 'ops 1p status'. It is the only command that still
// needs a selector: backup is always the whole machine, and restore takes
// server names positionally.
var onePasswordFilter string

var onePasswordCmd = &cobra.Command{
	Use:     "1p",
	Aliases: []string{"1password", "onepassword"},
	Short:   "把本机 SSH 凭据备份到 1Password，或还原到新机器",
	Long: `1Password 是备份与跨机器同步的目标，不是运行时依赖。

  ops 1p backup             把本机全部密钥/密码与整份 servers.yaml 上传
  ops 1p restore            把它们写回本地磁盘（并还原 servers.yaml）
  ops 1p status             查看哪些服务器有本地凭据
  ops 1p config             查看或修改记住的保险库与账号
  ops 1p doctor             端到端自检整条链路，不改动任何东西

凭据平时就放在本地磁盘：servers.yaml 里存的是密钥路径或明文密码，
'ops ssh' / 'ops exec' / 'ops cp' 直接读取，不与 1Password 发生任何往返。
正常连接过程中不会运行这里的任何东西，这正是那些命令从不弹授权框的原因。

密钥存放在标题为 opspulse_<server>_key 的 Login 条目里，位于一个自定义的
concealed 字段中；密码存放在标题为 opspulse_<server>_password 的 Login 条目里。
servers.yaml 本身作为共享条目 opspulse_inventory 备份。

通常你完全不必指定保险库：OpsPulse 用你通过 'ops 1p config --vault <name>'
记住的那个，其次是 $OP_VAULT，否则就是该账号唯一可见的保险库。
账号同理，$OP_ACCOUNT 优先于记住的值。`,
}

var onePasswordBackupCmd = &cobra.Command{
	Use:   "backup",
	Short: "把本机全部凭据与整份 servers.yaml 上传到 1Password",
	Long: `把本机的服务器清单与它持有的每一把私钥备份到 1Password。

整台机器装在一个以本机名命名的 Secure Note 里（opspulse_inventory_<hostname>），
这正是让备份只花三次 op 调用、而不是每台服务器一次的原因。它刻意是无条件的：
不做服务器选择，也没有跳过列表。

条目先读后写，本机已经读不出来的私钥会从上一份备份里沿用，而不是被丢掉——
本地文件没了之后，那份副本就是唯一的了。因其他原因导致的读取失败会中止备份，
而不是盲目覆盖。

servers.yaml 不会被改写。本地磁盘始终是唯一真相源，所以备份绝不会改变
'ops ssh' 的连接方式，也绝不会把一台本来能连的服务器变成依赖 1Password 解锁的服务器。

  ops 1p backup
  ops 1p backup --vault Private

仍然持有 'op://' 引用的服务器会被直接拒绝：上传它等于把一条陈旧引用推进备份。
先跑 'ops 1p restore' 把它迁移成本地凭据。

每台机器备份到各自的条目，所以两台机器永远不会互相覆盖，还原时再把它们并起来。
因此从备份里删除一台服务器只能手工做：先在本地删掉，再备份一次——
其他机器会一直保留它，直到它们也备份一次。`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runBackupToOnePassword(cmd.Context())
	},
}

var onePasswordRestoreCmd = &cobra.Command{
	Use:   "restore [server...]",
	Short: "把 1Password 里的凭据写回本地磁盘",
	Long: `把 1Password 里的凭据还原到本机。

不带参数时这是完整的脱困通道：先从各机器的备份条目还原 servers.yaml，
再把每台服务器的密钥写到 ~/.ssh/opspulse_<server>、把每个密码写进 servers.yaml。
一台新机器装好 1Password CLI 后，需要的就是这些。

  ops 1p restore               # 清单与全部凭据
  ops 1p restore web db-01     # 只还原这几台服务器的凭据
  ops 1p restore --yes         # 无人值守

带参数时只还原点名服务器的凭据，servers.yaml 保持不动。名字不在 servers.yaml 里
是报错而不是静默跳过，因为最常见的成因就是清单还没还原就先还原凭据。

密码只能以明文形式回来，所以只要本次还原会写入密码，OpsPulse 在写任何东西之前
都会先要求确认。在非交互 shell 里命令会直接拒绝而不是挂住，除非 --yes 事先给出答案。

本地密钥文件只在持有另一把密钥时才会被替换。比对按公钥进行，所以 1Password
以另一种格式返回的密钥会被认作同一把密钥，而不是被当成冲突；--force 可强制覆盖。

servers.yaml 里仍然持有 'op://' 引用的服务器会在还原过程中迁移为本地凭据。
这条兼容路径是临时的。`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRestoreFromOnePassword(cmd.Context(), args)
	},
}

var onePasswordStatusRemote bool

var onePasswordStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看哪些服务器有本地凭据、哪些已备份",
	Long: `查看每台服务器的凭据放在哪儿。

默认离线：只读 servers.yaml，因此从不联系 1Password、从不弹授权框。
传 --remote 会额外查询保险库，看哪些服务器有备份，这一步需要授权。`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runOnePasswordStatus(cmd.Context())
	},
}

var (
	onePasswordConfigVault   string
	onePasswordConfigAccount string
	onePasswordConfigUnset   bool
	onePasswordConfigOffline bool
)

var onePasswordConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "查看或修改记住的 1Password 保险库与账号",
	Long: `查看或修改 OpsPulse 记住的 1Password 默认值。

不带选项时打印当前生效的目标，以及当前账号能看到的账号与保险库。
带选项时记录一个默认值，让 'ops 1p backup' 不再每次都要求 --vault/--account。

传 --offline 可以在不联系 CLI 的情况下读取或修改记住的默认值，
保险库暂时解不开锁时正需要它。`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runOnePasswordConfig(cmd.Context())
	},
}

// resolveBackupVault picks the vault to write into, listing the vaults only when
// there is nothing recorded to go on.
//
// `op vault list` is a full round trip through the Desktop App, and the whole
// point of the backup is to spend as few of those as possible. A recorded
// preference is therefore taken at face value: if it has gone stale the write
// fails and names the vault, which is a clearer report than a list would have
// produced anyway.
//
// The vault that had to be discovered is remembered, so that only the first
// backup ever pays for the listing. Without this every run would cost three
// calls instead of two, which is exactly the cost this design exists to remove.
func resolveBackupVault(ctx context.Context, cli secret.CLI, explicitVault string) (string, error) {
	if vault := strings.TrimSpace(explicitVault); vault != "" {
		rememberOnePasswordSetting("vault", vault)
		return vault, nil
	}
	if candidates := rememberedVaultCandidates(); len(candidates) > 0 {
		return candidates[0], nil
	}
	vault, err := resolveAndValidateVault(ctx, cli, "", true)
	if err != nil {
		return "", err
	}
	rememberOnePasswordSetting("vault", vault)
	return vault, nil
}

// listVaults returns the names of every vault the current account can see.
func listVaults(ctx context.Context, cli secret.CLI) ([]string, error) {
	out, err := cli.Run(ctx, "vault", "list", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("%s\n\n%w", onePasswordFailureHint(cli, err), err)
	}

	var raw []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse 1Password vault list: %w", err)
	}
	names := make([]string, 0, len(raw))
	for _, v := range raw {
		if name := strings.TrimSpace(v.Name); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// resolveAndValidateVault decides which vault to operate on.
//
// Precedence, from strongest to weakest:
//  1. explicitVault: an explicit instruction, so an unknown name is a hard error.
//  2. $OP_VAULT, then the remembered setting: a name that no longer exists warns
//     and falls through rather than wedging every future command.
//  3. the only vault the account can see: no point demanding a choice.
//
// remember records an explicit choice as the new default. Backup wants that,
// since the vault is where items are created; restore's --vault only scopes a
// search, and remembering a read-only archive vault would silently move the
// next backup's target.
func resolveAndValidateVault(ctx context.Context, cli secret.CLI, explicitVault string, remember bool) (string, error) {
	names, err := listVaults(ctx, cli)
	if err != nil {
		return "", err
	}

	explicit := strings.TrimSpace(explicitVault)
	vault, warnings, err := chooseVault(names, explicit, rememberedVaultCandidates())
	for _, warning := range warnings {
		fmt.Printf("⚠️  %s\n", warning)
	}
	if err != nil {
		return "", err
	}
	if explicit != "" && remember {
		rememberOnePasswordSetting("vault", vault)
	}
	return vault, nil
}

// chooseVault picks the vault to operate on and returns a warning for every
// preference it had to skip because the vault no longer exists.
//
// The rules exist so that the common case needs no flag at all: a single
// accessible vault is chosen silently, while a genuine ambiguity is reported
// with a way out rather than a bare failure.
func chooseVault(names []string, explicit string, fallbacks []string) (vault string, warnings []string, err error) {
	if len(names) == 0 {
		return "", nil, fmt.Errorf("no 1Password vault is accessible with the current account")
	}

	if explicit != "" {
		if !containsString(names, explicit) {
			return "", nil, fmt.Errorf("vault %q was not found; available vaults: %s", explicit, strings.Join(names, ", "))
		}
		return explicit, nil, nil
	}

	for _, candidate := range fallbacks {
		if containsString(names, candidate) {
			return candidate, warnings, nil
		}
		warnings = append(warnings, fmt.Sprintf("1Password vault %q was not found; ignoring it. Available vaults: %s", candidate, strings.Join(names, ", ")))
	}

	if len(names) == 1 {
		return names[0], warnings, nil
	}
	return "", warnings, fmt.Errorf("several 1Password vaults are available (%s); pick one with --vault, or remember it once with 'ops 1p config --vault <name>'", strings.Join(names, ", "))
}

// rememberedVaultCandidates lists the exported and stored vault preferences, in
// the order they should be consulted. The environment comes first so that
// OP_VAULT keeps its usual meaning of "just for this shell".
func rememberedVaultCandidates() []string {
	candidates := []string{strings.TrimSpace(os.Getenv("OP_VAULT"))}
	if settings, err := secret.LoadSettings(); err == nil {
		candidates = append(candidates, settings.Vault)
	}
	return dedupeNonEmpty(candidates)
}

func containsString(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

func dedupeNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// rememberOnePasswordSetting records a default so the next run can omit the
// flag. Failures are reported but never fatal: not remembering a preference
// must not break an otherwise successful backup.
func rememberOnePasswordSetting(field, value string) {
	settings, err := secret.LoadSettings()
	if err != nil {
		fmt.Printf("⚠️  Could not read %s: %v\n", secret.SettingsPath(), err)
		return
	}
	if (field == "vault" && settings.Vault == value) || (field == "account" && settings.Account == value) {
		return
	}
	if field == "vault" {
		settings.Vault = value
	} else {
		settings.Account = value
	}
	if err := settings.Save(); err != nil {
		fmt.Printf("⚠️  Could not remember the %s: %v\n", field, err)
		return
	}
	fmt.Printf("💡 Remembered %s %q; future runs use it without --%s.\n", field, value, field)
}

func init() {
	onePasswordCmd.PersistentFlags().StringVar(&onePasswordAccount, "account", "", "1Password 账号（登录地址或 ID）；会被记住供后续运行使用")

	onePasswordBackupCmd.Flags().StringVar(&onePasswordVault, "vault", "", "要备份到的保险库（默认：记住的设置，其次 $OP_VAULT，最后是唯一可访问的保险库）")

	onePasswordRestoreCmd.Flags().StringVar(&onePasswordVault, "vault", "", "要从中还原的保险库（默认：记住的设置，其次 $OP_VAULT，最后是唯一可访问的保险库）")
	onePasswordRestoreCmd.Flags().BoolVarP(&onePasswordRestoreYes, "yes", "y", false, "不询问确认就把明文密码写入 servers.yaml")
	onePasswordRestoreCmd.Flags().BoolVar(&onePasswordRestoreForce, "force", false, "即使本地密钥文件持有另一把密钥也强制覆盖")
	onePasswordRestoreCmd.Flags().BoolVar(&onePasswordPreferLocal, "prefer-local", false, "所有清单冲突都以本机的 servers.yaml 为准")
	onePasswordRestoreCmd.Flags().BoolVar(&onePasswordPreferRemote, "prefer-remote", false, "所有清单冲突都以 1Password 备份为准")
	onePasswordRestoreCmd.ValidArgsFunction = completeServerNames

	onePasswordStatusCmd.Flags().StringVarP(&onePasswordFilter, "filter", "f", "", "按 label（key=val）、tag 或名称筛选服务器")
	onePasswordStatusCmd.Flags().BoolVar(&onePasswordStatusRemote, "remote", false, "额外查询 1Password 里哪些服务器有备份（需要授权）")

	onePasswordConfigCmd.Flags().StringVar(&onePasswordConfigVault, "vault", "", "把这个保险库记为默认目标")
	onePasswordConfigCmd.Flags().StringVar(&onePasswordConfigAccount, "account", "", "把这个账号记为默认值")
	onePasswordConfigCmd.Flags().BoolVar(&onePasswordConfigUnset, "unset", false, "忘掉记住的保险库与账号")
	onePasswordConfigCmd.Flags().BoolVar(&onePasswordConfigOffline, "offline", false, "只读写本地配置，不联系 1Password CLI")

	onePasswordCmd.AddCommand(
		onePasswordBackupCmd,
		onePasswordRestoreCmd,
		onePasswordStatusCmd,
		onePasswordConfigCmd,
		onePasswordDoctorCmd,
	)
	rootCmd.AddCommand(onePasswordCmd)
}
