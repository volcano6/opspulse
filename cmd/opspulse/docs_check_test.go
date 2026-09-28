package main

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// 本文件是 `make docs-check` 的文档对账器（断言一「docs/reference/cli.md 与命令树一致」在
// cli_reference_test.go）：链接与页内锚点必须可解析，Markdown 里出现的每条 `ops` 命令与其
// flag 必须能在命令树里解析，且命令树里每条命令都得在手写文档里露面。和生成器一样留在测试
// 代码里——生产包不必为文档多出导出 API，而 `make docs-check` 因此只是一条 `go test -run`。

// docsCheckIgnore 是行级逃生口：包含该标记的行、或该标记所在行的下一行，不参与命令/flag 校验。
const docsCheckIgnore = "docs-check: ignore"

// docsCheckSkip 列出不扫描的生成物：docs/reference/cli.md 按构造与命令树一致，扫它没有信息量。
var docsCheckSkip = map[string]bool{"docs/reference/cli.md": true}

// docsCheckEntryFiles 是 docs/ 之外同样纳入扫描的手写文档。
var docsCheckEntryFiles = []string{"README.md"}

type docsFile struct {
	rel  string // 仓库根相对路径，斜杠分隔
	body string
}

// docsCorpusRoot 返回语料根目录：默认仓库根。OPSPULSE_DOCS_CORPUS 可指向另一份语料，
// 用于把对账器对着修复前的旧文件跑一遍——门禁「有牙」的证据必须可复现。
func docsCorpusRoot() string {
	if root := os.Getenv("OPSPULSE_DOCS_CORPUS"); root != "" {
		return root
	}
	return filepath.Join("..", "..")
}

func loadDocsCorpus(t *testing.T) []docsFile {
	t.Helper()
	root := docsCorpusRoot()
	var files []docsFile
	read := func(rel string) {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("读取文档失败: %v", err)
		}
		files = append(files, docsFile{rel: rel, body: string(b)})
	}
	for _, rel := range docsCheckEntryFiles {
		read(rel)
	}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		if rel = filepath.ToSlash(rel); docsCheckSkip[rel] {
			return nil
		}
		read(rel)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 docs 失败: %v", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return files
}

// ── 断言二：相对链接与页内锚点 ────────────────────────────────────────────────

var markdownLinkRe = regexp.MustCompile(`\]\(([^)\s]+)\)`)

func TestDocsLinksResolve(t *testing.T) {
	corpus := loadDocsCorpus(t)
	slugCache := map[string]map[string]bool{}
	for _, f := range corpus {
		slugCache[f.rel] = headingSlugs(f.body)
	}
	slugsFor := func(rel string) map[string]bool {
		if s, ok := slugCache[rel]; ok {
			return s
		}
		b, err := os.ReadFile(filepath.Join(docsCorpusRoot(), filepath.FromSlash(rel)))
		if err != nil {
			return nil
		}
		s := headingSlugs(string(b))
		slugCache[rel] = s
		return s
	}

	for _, f := range corpus {
		for i, line := range strings.Split(f.body, "\n") {
			for _, m := range markdownLinkRe.FindAllStringSubmatch(line, -1) {
				raw := m[1]
				if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") ||
					strings.HasPrefix(raw, "mailto:") {
					continue
				}
				target, frag, _ := strings.Cut(raw, "#")
				if target == "" {
					target = path.Base(f.rel) // 页内锚点
				}
				resolved := path.Clean(path.Join(path.Dir(f.rel), target))
				if _, err := os.Stat(filepath.Join(docsCorpusRoot(), filepath.FromSlash(resolved))); err != nil {
					t.Errorf("%s:%d 链接目标不存在: %s", f.rel, i+1, raw)
					continue
				}
				if frag == "" || !strings.HasSuffix(resolved, ".md") {
					continue
				}
				if slugs := slugsFor(resolved); slugs != nil && !slugs[frag] {
					t.Errorf("%s:%d 页内锚点不存在: %s", f.rel, i+1, raw)
				}
			}
		}
	}
}

var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*$`)

// headingSlugs 复刻 GitHub 的标题锚点规则：小写、去掉标点（保留字母数字、下划线、连字符）、
// 空白转连字符。围栏代码块里的 `#` 注释不是标题，必须先跳过。
func headingSlugs(body string) map[string]bool {
	out := map[string]bool{}
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			out[slugify(m[2])] = true
		}
	}
	return out
}

func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-', r == ' ':
			b.WriteRune(r)
		}
	}
	return strings.ReplaceAll(b.String(), " ", "-")
}

// ── 断言三：命令与 flag 存在性 ────────────────────────────────────────────────

type docSegment struct {
	line int
	text string
}

// docCodeSegments 取出参与命令扫描的片段：围栏代码块内的整行，以及行内的反引号片段
// （手写表格里的命令与 flag 就住在行内片段里）。`$ `/`# ` 提示符与 `#` 注释前缀在此剥离。
func docCodeSegments(body string) []docSegment {
	var segs []docSegment
	inFence := false
	prevLine := ""
	for i, raw := range strings.Split(body, "\n") {
		line := i + 1
		if strings.Contains(raw, docsCheckIgnore) || strings.Contains(prevLine, docsCheckIgnore) {
			prevLine = raw
			continue
		}
		prevLine = raw
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			text := strings.TrimSpace(raw)
			switch {
			case strings.HasPrefix(text, "$ "), strings.HasPrefix(text, "> "):
				text = strings.TrimSpace(text[2:])
			case strings.HasPrefix(text, "#"):
				rest := strings.TrimSpace(text[1:])
				if !startsWithOpsName(rest) {
					continue // 注释行；`# ops ls` 这种 root 提示符才继续
				}
				text = rest
			}
			if text = stripShellComment(text); startsWithOpsName(text) {
				segs = append(segs, docSegment{line: line, text: text})
			}
			continue
		}
		for _, m := range inlineCodeRe.FindAllStringSubmatch(raw, -1) {
			text := strings.TrimSpace(m[1])
			if strings.HasPrefix(text, "$ ") {
				text = strings.TrimSpace(text[2:])
			}
			if startsWithOpsName(text) {
				segs = append(segs, docSegment{line: line, text: text})
			}
		}
	}
	return segs
}

// startsWithOpsName 要求片段以命令名开头：代码块里只有「像一次真实调用」的行才参与校验，
// 程序输出（`==> building ops and the op stub`）与散文里提到的命令名不算。
func startsWithOpsName(text string) bool {
	m := opsNameRe.FindStringSubmatchIndex(text)
	return m != nil && m[0] == 0 && m[2] == m[3] // group 1 为空 = 名字就在开头
}

var inlineCodeRe = regexp.MustCompile("`([^`]+)`")

// stripShellComment 截断行内 `#` 注释（引号内的 `#` 不算），免得注释里的 `-j` 被当成 flag。
func stripShellComment(line string) string {
	var quote rune
	for i, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return strings.TrimSpace(line[:i])
		}
	}
	return strings.TrimSpace(line)
}

// opsNameRe 匹配命令名并要求词边界：`opsfoo`、`--ops-x`、`opspulse:`（引用报错文案）都不算。
var opsNameRe = regexp.MustCompile(`(^|[^\pL\pN_.\-/])(?:ops|opspulse)(\s|$)`)

// docTokenTerminators 在命令片段里切断后续命令，避免把 `ops a && ops b --flag` 的 flag 算到 a 头上。
var docTokenTerminators = map[string]bool{
	"ops": true, "opspulse": true, "&&": true, "||": true, "|": true, ";": true,
}

func TestDocsCommandsExist(t *testing.T) {
	root := rootCmd
	root.InitDefaultHelpFlag()
	root.InitDefaultVersionFlag()
	indexes := map[*cobra.Command]*flagIndex{}

	for _, f := range loadDocsCorpus(t) {
		for _, seg := range docCodeSegments(f.body) {
			for _, loc := range opsNameRe.FindAllStringSubmatchIndex(seg.text, -1) {
				rest := seg.text[loc[1]:]
				cmd, token, ok := resolveCommandPath(root, rest)
				if !ok {
					t.Errorf("%s:%d 文档引用了不存在的命令 %q: ops %s", f.rel, seg.line, token, firstLine(rest))
					continue
				}
				idx, cached := indexes[cmd]
				if !cached {
					idx = newFlagIndex(cmd)
					indexes[cmd] = idx
				}
				for _, bad := range idx.unknown(flagTokens(rest)) {
					t.Errorf("%s:%d 命令 %q 上不存在 flag %q: ops %s", f.rel, seg.line, commandPath(cmd), bad, firstLine(rest))
				}
			}
		}
	}
}

// ── 断言四：命令树里每条命令都至少被手写文档提到一次 ──────────────────────────
// 生成的 docs/reference/cli.md 天然覆盖全部命令，因此不参与本断言；它盯的是手写文档：
// 新增、改名或删除命令后忘了同步文档，这里就会红（逃生口是 docsCheckIgnore）。

func TestDocsCommandsAreDocumented(t *testing.T) {
	root := rootCmd
	covered := map[*cobra.Command]bool{}
	for _, f := range loadDocsCorpus(t) {
		for _, seg := range docCodeSegments(f.body) {
			for _, loc := range opsNameRe.FindAllStringSubmatchIndex(seg.text, -1) {
				cmd, _, ok := resolveCommandPath(root, seg.text[loc[1]:])
				if !ok {
					continue
				}
				for c := cmd; c != nil; c = c.Parent() { // 提到子命令等于提到了它的父命令
					covered[c] = true
				}
			}
		}
	}
	for _, node := range flattenCommands(root) {
		if !covered[node.cmd] {
			t.Errorf("命令 %q 没有出现在任何手写文档里（docs/reference/cli.md 是生成物，不算数）", node.path)
		}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// cleanToken 去掉占位符与列表写法留下的包裹字符，让 `[--as` 还原成 `--as`；表格里的
// `\|` 还原成 `|`，`[--a\|--b]` 于是能拆成两个候选 flag。
func cleanToken(tok string) string {
	return strings.Trim(strings.ReplaceAll(tok, `\|`, "|"), "[](){}<>,;.")
}

func isPlaceholder(tok string) bool {
	return strings.ContainsAny(tok, "<>[]{}|")
}

// isCommandName 只认 ASCII 命令名：中文散文（`ops 自身的标志`）因此不会被当成子命令。
func isCommandName(tok string) bool {
	for _, r := range tok {
		if r > unicode.MaxASCII {
			return false
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
			return false
		}
	}
	return tok != ""
}

// resolveCommandPath 沿命令树解析 rest 开头的子命令路径，返回最深可解析的命令、卡住的
// token 与是否解析成功（第一个 token 就不是子命令 = 引用了不存在的命令）。
func resolveCommandPath(root *cobra.Command, rest string) (*cobra.Command, string, bool) {
	cmd := root
	for _, raw := range splitOpsTokens(rest) {
		if isPlaceholder(raw) {
			break
		}
		tok := cleanToken(raw)
		if tok == "" || tok == "--" || strings.HasPrefix(tok, "-") {
			break
		}
		if !isCommandName(tok) {
			break
		}
		child := findChild(cmd, tok)
		if child == nil {
			if cmd == root {
				return nil, tok, false
			}
			break
		}
		cmd = child
	}
	return cmd, "", true
}

// splitOpsTokens 按 shell 习惯切词：引号内的空格与 `|` 不切（`"docker ps -q | wc -l"` 是一个
// 参数），并在此切断后续命令，避免把 `ops a && ops b --flag` 的 flag 算到 a 头上。
func splitOpsTokens(rest string) []string {
	var tokens []string
	var cur strings.Builder
	var quote rune
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range rest {
		switch {
		case quote != 0:
			cur.WriteRune(r)
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
			cur.WriteRune(r)
		case unicode.IsSpace(r):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	for i, tok := range tokens {
		if docTokenTerminators[tok] {
			return tokens[:i]
		}
	}
	return tokens
}

func findChild(cmd *cobra.Command, name string) *cobra.Command {
	for _, child := range childCommands(cmd) {
		if child.Name() == name {
			return child
		}
		for _, alias := range child.Aliases {
			if alias == name {
				return child
			}
		}
	}
	return nil
}

// flagTokens 返回该片段里 `--` 之前出现的 flag token（`--` 之后是远端命令的参数）。
func flagTokens(rest string) []string {
	var out []string
	for _, raw := range splitOpsTokens(rest) {
		if raw == "--" {
			break
		}
		tok := cleanToken(raw)
		if strings.HasPrefix(tok, "-") && len(tok) > 1 {
			out = append(out, tok)
		}
	}
	return out
}

type flagIndex struct {
	longs  map[string]bool
	shorts map[rune]bool
}

func newFlagIndex(cmd *cobra.Command) *flagIndex {
	cmd.InitDefaultHelpFlag()
	cmd.InitDefaultVersionFlag()
	_ = cmd.LocalFlags() // 触发 mergePersistentFlags：把父命令的持久 flag 合进 Flags()
	idx := &flagIndex{longs: map[string]bool{}, shorts: map[rune]bool{}}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		idx.longs[f.Name] = true
		if f.Shorthand != "" {
			for _, r := range f.Shorthand {
				idx.shorts[r] = true
			}
		}
	})
	return idx
}

// unknown 返回命令上不存在的 flag：长名按 `=` 前的名字比对，短名逐个字符比对（`-rf` 展开，
// 短名后的数字视为取值）。
func (idx *flagIndex) unknown(tokens []string) []string {
	var bad []string
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "--") {
			name := strings.TrimPrefix(tok, "--")
			name, _, _ = strings.Cut(name, "=")
			for _, alt := range strings.Split(name, "|") { // `[--prefer-local\|--prefer-remote]` 这类二选一写法
				alt = strings.TrimPrefix(alt, "--")
				if alt != "" && !idx.longs[alt] {
					bad = append(bad, "--"+alt)
				}
			}
			continue
		}
		for i, r := range tok[1:] {
			if idx.shorts[r] {
				continue
			}
			if i > 0 && unicode.IsDigit(r) {
				break // 形如 -j5：数字是取值
			}
			bad = append(bad, "-"+string(r))
			break
		}
	}
	return bad
}
