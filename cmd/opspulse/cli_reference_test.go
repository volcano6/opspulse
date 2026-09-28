package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// 本文件是 docs/reference/cli.md 的唯一生成器。生成器刻意留在测试代码里：生产包
// 不必为一份文档多出导出 API，而 `go test -update` 天然把「重新生成」与「逐字节校验」
// 放在同一条命令上——默认路径（不带 -update）只比对，所以生成后工作区必须干净。

// updateCLIReference 由 `make docs-gen` 传入，把命令树重新写入 docs/reference/cli.md。
var updateCLIReference = flag.Bool("update", false, "regenerate docs/reference/cli.md from the cobra command tree")

// cliReferenceFile 相对本包目录（go test 的工作目录）。
const cliReferenceFile = "../../docs/reference/cli.md"

func TestCLIReferenceUpToDate(t *testing.T) {
	want := renderCLIReference(rootCmd)

	if *updateCLIReference {
		if err := os.WriteFile(cliReferenceFile, []byte(want), 0o644); err != nil {
			t.Fatalf("写入 %s 失败: %v", cliReferenceFile, err)
		}
		return
	}

	got, err := os.ReadFile(cliReferenceFile)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v\n运行 `make docs-gen` 生成它", cliReferenceFile, err)
	}
	if string(got) != want {
		t.Fatalf("%s 与 CLI 命令树不一致——运行 `make docs-gen` 重新生成", cliReferenceFile)
	}
}

// TestCLIReferenceRenderingIsDeterministic 守住「连续两次生成逐字节一致」：命令与 flag
// 都按名字排序，任何一处漏排序都会被 Go 的随机 map 遍历顺序抓到。
func TestCLIReferenceRenderingIsDeterministic(t *testing.T) {
	first := renderCLIReference(rootCmd)
	for i := 2; i <= 4; i++ {
		if got := renderCLIReference(rootCmd); got != first {
			t.Fatalf("第 %d 次渲染与首次不一致：生成结果依赖遍历顺序", i)
		}
	}
}

// TestCLIReferenceIgnoresFrameworkArtifacts 守住「生成结果与执行顺序无关」：-h/--help 与
// -v/--version 由 cobra 在 Execute 时注入，而本进程里别的测试会执行命令。跑一次真实的
// 命令触发注入后，渲染结果必须逐字节不变，否则 `make docs-gen` 与 `make test` 会各自
// 产出不同的文件。
func TestCLIReferenceIgnoresFrameworkArtifacts(t *testing.T) {
	before := renderCLIReference(rootCmd)

	prevOut, prevErr := rootCmd.OutOrStdout(), rootCmd.ErrOrStderr()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{"--help"})
	err := rootCmd.Execute()
	rootCmd.SetOut(prevOut)
	rootCmd.SetErr(prevErr)
	rootCmd.SetArgs(nil)
	if err != nil {
		t.Fatalf("rootCmd.Execute(--help) error: %v", err)
	}

	if after := renderCLIReference(rootCmd); after != before {
		t.Fatal("执行过命令后渲染结果变了：cobra 注入的 help 命令或 -h/--help、-v/--version 未被过滤")
	}
}

type cliCommandNode struct {
	cmd    *cobra.Command
	path   string
	parent string
}

// renderCLIReference 把整棵命令树渲染成 docs/reference/cli.md 的内容。纯函数：
// 只读 rootCmd，不写文件、不依赖时间或版本。
func renderCLIReference(root *cobra.Command) string {
	nodes := flattenCommands(root)

	var b strings.Builder
	b.WriteString("<!-- 由 `make docs-gen` 从 cmd/opspulse 的 cobra 命令树生成，请勿手工编辑。 -->\n\n")
	b.WriteString("# 命令行参考 (CLI Reference)\n\n")
	b.WriteString("本页是 `ops` 全部命令与 flag 的权威清单，由 `cmd/opspulse` 的 cobra 命令树直接生成，\n")
	b.WriteString("随代码更新。`opspulse` 是 `ops` 的别名，两者完全等价。重新生成：`make docs-gen`。\n\n")
	b.WriteString("---\n\n")

	b.WriteString("## 1. 命令总览 (Command overview)\n\n")
	b.WriteString("| 命令 | 说明 |\n| --- | --- |\n")
	for _, n := range nodes {
		fmt.Fprintf(&b, "| %s | %s |\n", mdCell(code(n.useLine())), mdCell(n.cmd.Short))
	}

	b.WriteString("\n---\n\n")
	b.WriteString("## 2. 命令与 flag 详情 (Commands and flags)\n")
	for _, n := range nodes {
		writeCommandDetail(&b, n)
	}
	return b.String()
}

// flattenCommands 按深度优先（同级按名字排序）展开命令树：根命令在前，随后是每个
// 分组父命令及其叶子，因此总览表读起来就是命令树本身。
func flattenCommands(root *cobra.Command) []cliCommandNode {
	var nodes []cliCommandNode
	var walk func(cmd *cobra.Command, path, parent string)
	walk = func(cmd *cobra.Command, path, parent string) {
		nodes = append(nodes, cliCommandNode{cmd: cmd, path: path, parent: parent})
		for _, child := range childCommands(cmd) {
			walk(child, path+" "+child.Name(), path)
		}
	}
	walk(root, root.Name(), "")
	return nodes
}

// childCommands 返回要写进参考的子命令，剔除 cobra 的框架命令（help 与两个补全内部
// 命令）。help 命令由 main.init 主动创建，因此它与命令树是否执行过无关；显式过滤是
// 为了保证生成结果永远与执行顺序无关。
func childCommands(cmd *cobra.Command) []*cobra.Command {
	children := make([]*cobra.Command, 0, len(cmd.Commands()))
	for _, child := range cmd.Commands() {
		if isFrameworkCommand(child) {
			continue
		}
		children = append(children, child)
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	return children
}

// isFrameworkCommand 认出去掉 cobra 的 help 命令与两个补全内部命令：它们不是
// OpsPulse 的命令面。按名字判断而不是按 Short 文案——help 命令的说明已在 main.init
// 里改成中文。
func isFrameworkCommand(cmd *cobra.Command) bool {
	switch cmd.Name() {
	case "help":
		return true
	case cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
		return true
	}
	return false
}

// ownFlags 返回注册在该命令上的 flag（不含父命令继承来的持久 flag），按名字排序。
func ownFlags(cmd *cobra.Command) []*pflag.Flag {
	var flags []*pflag.Flag
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if isFrameworkFlag(f) {
			return
		}
		flags = append(flags, f)
	})
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
	return flags
}

// isFrameworkFlag 认出去掉 -h/--help 与 -v/--version 这类 cobra 内建 flag：它们不是
// OpsPulse 的命令面。按 cobra 自己打的注解判断，与说明文案无关——说明文案在渲染时由
// main.localizeFrameworkFlags 改成中文，注解是唯一稳定的识别依据。
func isFrameworkFlag(f *pflag.Flag) bool {
	return len(f.Annotations[cobra.FlagSetByCobraAnnotation]) > 0
}

func writeCommandDetail(b *strings.Builder, n cliCommandNode) {
	fmt.Fprintf(b, "\n### %s\n\n", code(n.useLine()))

	if len(n.cmd.Aliases) > 0 {
		aliases := make([]string, 0, len(n.cmd.Aliases))
		for _, alias := range n.cmd.Aliases {
			aliases = append(aliases, code(strings.TrimSpace(n.parent+" "+alias)))
		}
		fmt.Fprintf(b, "别名：%s\n\n", strings.Join(aliases, "、"))
	}

	desc := n.cmd.Long
	if strings.TrimSpace(desc) == "" {
		desc = n.cmd.Short
	}
	b.WriteString(renderProse(desc))
	b.WriteString("\n\n")

	if flags := ownFlags(n.cmd); len(flags) > 0 {
		b.WriteString("| flag | 简写 | 默认值 | 说明 |\n| --- | --- | --- | --- |\n")
		for _, f := range flags {
			fmt.Fprintf(b, "| %s | %s | %s | %s |\n",
				code("--"+f.Name), shorthandCell(f), mdCell(defaultCell(f)), mdCell(f.Usage))
		}
	} else {
		b.WriteString("该命令没有自己的 flag。\n")
	}

	if line := inheritedFlagLine(n.cmd); line != "" {
		b.WriteString("\n" + line + "\n")
	}
}

func shorthandCell(f *pflag.Flag) string {
	if f.Shorthand == "" {
		return "-"
	}
	return code("-" + f.Shorthand)
}

func defaultCell(f *pflag.Flag) string {
	if f.DefValue == "" {
		return "(空)"
	}
	return code(f.DefValue)
}

// inheritedFlagLine 说明该命令还能用哪些来自父命令的持久 flag，并指出定义处——每个
// flag 只在它自己的小节里有一行表格，这里只做指路，不重复默认值与说明。
func inheritedFlagLine(cmd *cobra.Command) string {
	names := []string{}
	cmd.InheritedFlags().VisitAll(func(f *pflag.Flag) {
		if !isFrameworkFlag(f) {
			names = append(names, f.Name)
		}
	})
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		owner := ""
		for p := cmd.Parent(); p != nil; p = p.Parent() {
			if p.PersistentFlags().Lookup(name) != nil {
				owner = commandPath(p)
				break
			}
		}
		parts = append(parts, fmt.Sprintf("%s（定义于 %s）", code("--"+name), code(owner)))
	}
	return "继承的持久 flag：" + strings.Join(parts, "、") + "。"
}

// commandPath 返回命令的完整调用路径（如 "ops backup run"）。
func commandPath(cmd *cobra.Command) string {
	var parts []string
	for c := cmd; c != nil; c = c.Parent() {
		parts = append([]string{c.Name()}, parts...)
	}
	return strings.Join(parts, " ")
}

// useLine 是命令的完整调用写法：路径 + Use 里的参数部分（不含 cobra 自动补的 [flags]）。
func (n cliCommandNode) useLine() string {
	return n.path + strings.TrimPrefix(n.cmd.Use, n.cmd.Name())
}

// renderProse 把 cobra 的 Long 文本按 Markdown 输出，并把缩进块（命令示例）放进围栏
// 代码块——否则 Markdown 会把连续行折叠成一行，示例就不可读了。缩进块内部的空行只要
// 后面仍是缩进行就留在块内，免得一段示例被切成好几个围栏。
func renderProse(text string) string {
	var out []string
	inCode := false
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		if inCode && strings.TrimSpace(line) == "" && nextNonBlankIsIndented(lines, i) {
			out = append(out, "")
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if !inCode {
				out = append(out, "```")
				inCode = true
			}
			out = append(out, line)
			continue
		}
		if inCode {
			out = append(out, "```")
			inCode = false
		}
		out = append(out, line)
	}
	if inCode {
		out = append(out, "```")
	}
	return strings.Join(out, "\n")
}

func nextNonBlankIsIndented(lines []string, i int) bool {
	for _, line := range lines[i+1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
	}
	return false
}

// code 把 s 包成 Markdown 行内代码；内容自带反引号时自动加长围栏，避免提前闭合。
func code(s string) string {
	fence := "`"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	return fence + s + fence
}

// mdCell 转义表格单元格里的内容：竖线会截断单元格，换行会截断表格。
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}
