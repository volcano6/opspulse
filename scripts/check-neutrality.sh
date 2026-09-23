#!/usr/bin/env bash
# 中性化门禁：扫描所有「已跟踪文件」，拒绝会被公开的真实环境信息。
# 由 .github/workflows/ci.yaml 与 scripts/ci.sh（make ci）调用；退出码 0=通过。
#
# 设计原则（与 CONTRIBUTING.md「中性化约定」配套）：
#   1. 本脚本自身不得包含任何真实值。词表一旦写入真实 IP / 邮箱 / 雇主名，脚本本身就成了泄漏源，
#      所以这里只做**形状**判定：地址段、路径形态、密钥块、库名白名单。
#   2. 只做高价值、低误报的检查。刻意不做的几类（会大面积误伤）：
#      · 通用 FQDN 正则 —— 会命中 Go import path 与文件名；
#      · base64 长串 —— 会命中 go.sum 里每一条 h1: 行；
#      · 裸数字串 —— 会命中字节数、端口与 semver；
#      · IPv6 字面量 —— 仓库内只有 RFC 3849 / 2606 夹具，形状规则净收益为负。
#   3. 真实值词表（雇主名、真实公网 IP 等）只存在于本地守卫 .trellis/tools/guard.sh，绝不入仓库。
set -uo pipefail

cd "$(dirname "$0")/.." || exit 2
git rev-parse --git-dir >/dev/null 2>&1 || { echo "neutrality: 需要 git 仓库（CI 中请先 checkout）" >&2; exit 2; }

mapfile -t FILES < <(git ls-files)
if [ "${#FILES[@]}" -eq 0 ]; then
  echo "neutrality: 没有已跟踪文件，跳过"
  exit 0
fi

VIOLATIONS=0

# 按「文件:行号:取值」逐条判定；$3 为允许清单正则，命中即放过。
rule() {
  local re="$1" desc="$2" allow="$3"
  local out line file rest ln val
  out="$(grep -a -n -H -o -E "$re" "${FILES[@]}" 2>/dev/null || true)"
  [ -z "$out" ] && return 0
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    file="${line%%:*}"; rest="${line#*:}"; ln="${rest%%:*}"; val="${rest#*:}"
    if [ -n "$allow" ] && printf '%s' "$val" | grep -qE "$allow"; then continue; fi
    printf '  ✗ %s:%s  %s —— %s\n' "$file" "$ln" "$desc" "$val"
    VIOLATIONS=$((VIOLATIONS + 1))
  done <<< "$out"
}

# 1) IPv4：只允许文档段 / 私网段 / 回环 / 链路本地 / 全零，外加仓库既有的通用占位夹具。
#    1.1.1.1 是公共 DNS；1.1.1.2-3、1.2.3.x、2.2.2.2、3.3.3.3、5.6.7.8、9.9.9.9 是测试夹具里
#    长期使用的「一眼假」地址。它们不含任何环境信息；其余任何公网地址一律拦下。
rule '([0-9]{1,3}\.){3}[0-9]{1,3}' '非文档段 IPv4' \
  '^(10\.|127\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.|192\.0\.2\.|198\.51\.100\.|203\.0\.113\.|169\.254\.|0\.|255\.|1\.1\.1\.[0-9]+$|1\.2\.3\.[0-9]+$|2\.2\.2\.2$|3\.3\.3\.3$|5\.6\.7\.8$|9\.9\.9\.9$)'

# 2) 消费者邮箱域（真实邮箱必然落在这些域上；example.com 之类不在列）
rule '@(gmail|googlemail|163|126|qq|foxmail|outlook|hotmail|live|yahoo|sina|sohu|icloud|protonmail)\.(com|cn|net|org)' \
  '真实邮箱域' ''

# 3) 家目录 / 用户目录里的真实用户名（`...` 是文档里的省略号占位）
rule '(/home/|/Users/|[A-Za-z]:[\\/]Users[\\/])[A-Za-z0-9._-]+' '非中性用户目录' \
  '^([A-Za-z]:[\\/]Users[\\/]|/(home|Users)/)(u|user[A-Za-z0-9._-]*|test|name|username|Username|Public|Default|runner|root|\.+)$'

# 4) 私钥块：PEM 头后紧跟真实 base64 正文（`...` 占位或极短正文不算）
KEYOUT="$(awk '
  FNR == 1 { pend = 0 }
  /-----BEGIN [A-Z ]*PRIVATE KEY-----/ { pend = 1; next }
  pend && /^[A-Za-z0-9+\/]{60,}={0,2}$/ { printf "  ✗ %s:%d  私钥正文疑似真实密钥\n", FILENAME, FNR; pend = 0; next }
  pend && /[^[:space:]]/ { pend = 0 }
' "${FILES[@]}" 2>/dev/null || true)"
if [ -n "$KEYOUT" ]; then
  printf '%s\n' "$KEYOUT"
  VIOLATIONS=$((VIOLATIONS + $(printf '%s\n' "$KEYOUT" | grep -c '✗')))
fi

# 5) op:// 库名：只允许默认库名与占位（`...` 是注释里的省略号）
rule 'op://[A-Za-z0-9._<>-]+' '非默认 op:// 库名' \
  '^op://(<[A-Za-z0-9._-]*>|\.+|vault|Vault|VAULT|example|acme|Personal|Private|Work|Other|Employee|Shared|Team|personal|private|work|other|employee|shared|team)$'

if [ "$VIOLATIONS" -eq 0 ]; then
  echo "✓ 中性化检查通过（已扫描 ${#FILES[@]} 个已跟踪文件）"
  exit 0
fi

echo "" >&2
echo "✗ 中性化检查失败：$VIOLATIONS 处。修法见 CONTRIBUTING.md「中性化约定」。" >&2
exit 1
