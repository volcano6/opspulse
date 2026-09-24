#!/bin/bash
#
# 本地门禁入口。所有检查都定义在 Makefile 里（单一事实源），本脚本只做透传——
# 它**不再内联任何命令**。历史教训：ci.sh 自己内联了一套
# （revive/errcheck/ineffassign/gosec/staticcheck + 三平台 vet + 固定 linux 构建），
# 而 CI 用 golangci-lint + 单平台构建，两边从此各走各的，谁也不知道对方在查什么。
#
# 保留脚本本身是因为既有文档、贡献者习惯与外部脚本都直接调用
# `bash scripts/ci.sh`；现在它与 `make ci` 完全等价。
set -euo pipefail

cd "$(dirname "$0")/.."
echo "scripts/ci.sh -> make ci（检查定义见 Makefile）"
exec make ci
