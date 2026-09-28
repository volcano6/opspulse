#!/usr/bin/env bash
# 从 conventional commit 生成 CHANGELOG 章节。
#
# 为什么存在：手写 CHANGELOG 是每个提交都要付的税，而提交标题本来就遵循
# Conventional Commits（见 CONTRIBUTING.md），所以变更记录可以推导出来。
# 本脚本只做推导，不做判断——版本号与发布日期由调用方给定。
#
# 用法：
#   scripts/gen-changelog.sh                          # 上一个 tag..HEAD（HEAD 带 tag 时自动退一位），打印到 stdout
#   scripts/gen-changelog.sh --version 0.5.0          # 指定版本号
#   scripts/gen-changelog.sh --since v0.3.0           # 指定区间起点
#   scripts/gen-changelog.sh --write                  # 插入 CHANGELOG.md（第一个 "## [" 之前）
#
# 调用方：`make changelog`（本地发版）与 .github/workflows/release.yaml（release body）。
# 退出码 0=成功，2=参数或环境错误。
set -uo pipefail

cd "$(dirname "$0")/.." || exit 2
git rev-parse --git-dir >/dev/null 2>&1 || { echo "gen-changelog: 需要 git 仓库" >&2; exit 2; }

since=""
version=""
date="$(date -u +%Y-%m-%d)"
write=0

while [ $# -gt 0 ]; do
  case "$1" in
    --since)   since="${2:?--since 需要参数}"; shift 2 ;;
    --version) version="${2:?--version 需要参数}"; shift 2 ;;
    --date)    date="${2:?--date 需要参数}"; shift 2 ;;
    --write)   write=1; shift ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "gen-changelog: 未知参数 $1（-h 查看用法）" >&2; exit 2 ;;
  esac
done

# 默认区间起点 = 最近一个 tag；仓库还没有 tag 时取全部历史。
# HEAD 自己带着 tag 时要退到 HEAD 之前的那一个：release.yaml 正是这个场景（tag 先推、
# workflow 再从被 tag 的提交上生成 body），此时「最近一个 tag」就是本次要发的版本，
# 区间会塌成 <tag>..HEAD = 空，release body 只剩一句「本次区间没有提交」。
if [ -z "$since" ]; then
  if git describe --tags --exact-match HEAD >/dev/null 2>&1; then
    since="$(git describe --tags --abbrev=0 HEAD^ 2>/dev/null || true)"
  else
    since="$(git describe --tags --abbrev=0 2>/dev/null || true)"
  fi
fi
range="HEAD"
[ -n "$since" ] && range="${since}..HEAD"

# 版本号必须由调用方给定：区间起点是「上一个 tag」，推不出本次要发的版本。
[ -z "$version" ] && version="未发布 (Unreleased)"

# 记录分隔符 0x1e、字段分隔符 0x1f：都不会出现在提交标题里。
SEP_REC="$(printf '\036')"
SEP_FLD="$(printf '\037')"

notes="$(
  git log "$range" --no-merges --pretty=format:"%h${SEP_FLD}%s${SEP_FLD}%b${SEP_REC}" 2>/dev/null |
    awk -v RS="$SEP_REC" -v FS="$SEP_FLD" \
      -v SECTIONS='feat:新增|fix:修复|perf:性能|refactor:重构|revert:回滚|docs:文档|chore:构建与杂项' '
      BEGIN {
        n = split(SECTIONS, ord, "|")
        for (i = 1; i <= n; i++) {
          split(ord[i], kv, ":")
          type_of[i] = kv[1]
          label[kv[1]] = kv[2]
        }
      }
      NF == 0 { next }
      {
        # `--pretty=format:` 在记录之间插入换行，会落在下一个记录的开头。
        sub(/^[\r\n]+/, "", $0)

        hash = $1; subject = $2; body = $3
        if (subject == "") next

        prefix = ""; desc = subject
        if (match(subject, /^[A-Za-z]+(\([^)]*\))?!?:[ \t]+/)) {
          prefix = substr(subject, 1, RLENGTH)
          desc = substr(subject, RLENGTH + 1)
        }

        # 无法识别的类型（含没有 type 前缀的标题）归入最后一节。
        type = type_of[n]
        if (prefix != "") {
          t = prefix
          sub(/[^A-Za-z].*$/, "", t)
          if (t in label) type = t
        }

        scope = ""
        if (match(prefix, /\([^)]*\)/)) scope = substr(prefix, RSTART + 1, RLENGTH - 2)

        bullet = "- "
        if (prefix ~ /!:/ || body ~ /BREAKING[ -]CHANGE/) bullet = bullet "**破坏性：** "
        if (scope != "") bullet = bullet "**" scope "** "
        bullet = bullet desc " (`" hash "`)"

        if (type in list) list[type] = list[type] "\n" bullet
        else list[type] = bullet
      }
      END {
        any = 0
        for (i = 1; i <= n; i++) {
          t = type_of[i]
          if (t in list) {
            any = 1
            print "### " label[t]
            print ""
            print list[t]
            print ""
          }
        }
        if (!any) print "本次区间没有可归纳的提交。"
      }
    '
)"

if [ -z "$notes" ]; then
  notes="本次区间没有提交。"
fi

section="## [${version}] - ${date}

${notes}"

if [ "$write" -eq 0 ]; then
  printf '%s\n' "$section"
  exit 0
fi

target="CHANGELOG.md"
[ -f "$target" ] || { echo "gen-changelog: 找不到 $target" >&2; exit 2; }

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
if grep -q '^## \[' "$target"; then
  awk -v block="$section" '
    !done && /^## \[/ { print block; print ""; done = 1 }
    { print }
  ' "$target" > "$tmp"
else
  { cat "$target"; printf '\n%s\n' "$section"; } > "$tmp"
fi
mv "$tmp" "$target"
echo "gen-changelog: 已写入 ${target}（版本 ${version}）"
