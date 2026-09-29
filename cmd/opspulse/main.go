// Package main is the entry point for the OpsPulse CLI.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/logger"
	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/version"
)

var debugFlag bool

var rootCmd = &cobra.Command{
	Use:     "ops",
	Aliases: []string{"opspulse"},
	Short:   "个人开发者的自托管服务器自动化与备份编排",
	Long: `Ops —— 自托管服务器自动化、备份编排与安全运维。

环境变量：
  OPSPULSE_HOME                覆盖 OpsPulse 主目录（配置与数据）。
  OPSPULSE_OP_PATH             强制指定 'ops 1p' 驱动的 1Password CLI 可执行文件。
  OPSPULSE_KNOWN_HOSTS         为内置 SSH 客户端指定其他 known_hosts 文件。
  OPSPULSE_TRUST_NEW_HOST_KEY  设为 1，首次连接时接受未知主机密钥。
  OP_VAULT                     默认 1Password 保险库，覆盖已记住的值。
  OP_ACCOUNT                   默认 1Password 账户，覆盖已记住的值。`,
	Version:      version.Version,
	SilenceUsage: true,
	PersistentPreRun: func(_ *cobra.Command, _ []string) {
		logger.Setup(debugFlag)
	},
}

// usageTemplate 是 cobra 默认用法模板的中文版：结构与默认模板逐字对应，只翻译了标签文字。
const usageTemplate = `用法：{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

别名：
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

示例：
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

可用命令：{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{.Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

其他命令：{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

选项：
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

全局选项：
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

其他帮助主题：{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

用 "{{.CommandPath}} [command] --help" 查看某个命令的详细用法。{{end}}
`

// versionLine is the one place the version is rendered, so that 'ops version'
// and 'ops --version' cannot drift apart.
func versionLine() string {
	return fmt.Sprintf("ops %s (commit: %s, built: %s)", version.Version, version.Commit, version.Date)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "打印版本信息",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Println(versionLine())
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&debugFlag, "debug", false, "输出详细的调试日志")

	rootCmd.SetUsageTemplate(usageTemplate)
	rootCmd.SetVersionTemplate(versionLine() + "\n")
	rootCmd.AddCommand(versionCmd)

	// 内置 help 命令的 Short/Long 同样是英文，就地覆盖文案。
	rootCmd.InitDefaultHelpCmd()
	if helpCmd, _, err := rootCmd.Find([]string{"help"}); err == nil {
		helpCmd.Short = "查看任意命令的帮助"
		helpCmd.Long = `查看任意命令的帮助。

输入 "ops help [命令路径]" 查看该命令的完整用法，例如 "ops help backup run"。`
	}

	// -h/--help 与 -v/--version 由 cobra 在执行命令时按需注入，说明写死为英文的
	// "help for <命令>"；注入发生在解析 flag 之前，没有钩子可以拦。于是改为在渲染
	// 用法与帮助时改写这两条 flag 的说明：注入逻辑保持上游原样——每个命令一份局部
	// flag，互不影响。自己注册成持久 flag 会让 `--help` 的取值留在命令树上，后续
	// 任何一次 Execute 都会被它短路（测试里就是连跑多个命令的场景）。
	usageFunc, helpFunc := rootCmd.UsageFunc(), rootCmd.HelpFunc()
	rootCmd.SetUsageFunc(func(c *cobra.Command) error {
		localizeFrameworkFlags(c)
		return usageFunc(c)
	})
	rootCmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		localizeFrameworkFlags(c)
		helpFunc(c, args)
	})
}

// localizeFrameworkFlags 把 cobra 注入的 -h/--help 与 -v/--version 说明改成中文。
// 只认 cobra 自己打的注解，用户自定义的同名 flag 不受影响。
func localizeFrameworkFlags(c *cobra.Command) {
	for _, fw := range []struct{ name, usage string }{
		{"help", "显示帮助信息"},
		{"version", "显示版本信息"},
	} {
		f := c.Flags().Lookup(fw.name)
		if f == nil {
			continue
		}
		if _, injected := f.Annotations[cobra.FlagSetByCobraAnnotation]; injected {
			f.Usage = fw.usage
		}
	}
}

func main() {
	if os.Getenv(askpassHelperFlag) == "1" {
		prompt := ""
		if len(os.Args) > 1 {
			prompt = os.Args[1]
		}
		password, err := readSSHAskpassPassword(prompt)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_, _ = fmt.Fprint(os.Stdout, password)
		return
	}
	if err := rootCmd.Execute(); err != nil {
		os.Exit(handleRootError(err, os.Stderr))
	}
}

// handleRootError decides the process exit code for a root-command failure and,
// when the failure is a missing 1Password CLI, appends the install hint to
// stderr before returning. The hint is appended here so every path that
// references op:// gets it, not only the `ops 1p` subcommands that already print
// it themselves.
func handleRootError(err error, stderr io.Writer) int {
	if errors.Is(err, secret.ErrCLINotFound) {
		fmt.Fprintf(stderr, "💡 %s\n", secret.InstallHint())
	}
	return commandExitCode(err)
}

func commandExitCode(err error) int {
	var executionErr *executor.ExecutionError
	if errors.As(err, &executionErr) && executionErr.ExitCode > 0 {
		return executionErr.ExitCode
	}
	// A child process that fails reaches here as a bare *exec.ExitError, for
	// example 'ops ssh <name> --exec ...'. Report its status instead of a
	// generic 1 so callers can branch on the remote command's exit code.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
		return exitErr.ExitCode()
	}
	return 1
}
