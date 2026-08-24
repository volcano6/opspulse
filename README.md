# OpsPulse

[![CI](https://github.com/volcano6/opspulse/actions/workflows/ci.yaml/badge.svg)](https://github.com/volcano6/opspulse/actions/workflows/ci.yaml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/volcano6/opspulse)](https://go.dev/)
[![License](https://img.shields.io/github/license/volcano6/opspulse)](LICENSE)

**Infrastructure Action Runner** — 面向个人开发者的自托管服务器全生命周期与无缝迁移平台。

---

## 🎯 为什么需要 OpsPulse？

管理多台云服务器（VPS）时，个人开发者经常遇到以下痛点：

* 🔄 **重复初始化配置**：每次开通新 VPS 都要手动敲一遍重复的 curl、apt、常用工具和安全配置。
* 📦 **备份脚本散落各处**：各台服务器上的备份脚本和 cron 任务缺乏统一监控，备份成功与否无感知。
* 🚚 **机器到期迁移痛苦**：VPS 到期更换服务商时，数据导出、环境安装、路径调整、证书配置和重新上线流程繁琐且易出错。
* 🔒 **敏感凭据散落**：各种密码和 API Token 散落在各个服务器的明文 `.env` 文件或记忆中。

**OpsPulse** 采用单一可执行二进制文件，提供声明式服务器清单、有状态业务资产（Asset）管理、可复用的 Shell 模板、统一调度的 restic 备份与跨机重映射还原，并将所有执行历史与状态结构化持久化至本地 SQLite 数据库。

---

## ✨ 核心特性

- **🚀 服务器清单管理**：使用简洁的 YAML (`servers.yaml`) 统一管理所有服务器，支持标签、SSH 密钥认证、密码备选与自定义端口。
- **🧩 结构化业务资产 (Asset)**：支持 Docker Compose、Volume、数据库 Dump、Nginx 站点等有状态资产，以稳定全局 ID 标识，支持跨机灵活路径重映射（Remap）。
- **📜 脚本模板系统**：Shell 脚本支持 YAML Frontmatter 元数据头部。内置开箱即用的官方模板（`base`、`docker`、`security`、`restic`），支持自定义模板与同名优先覆盖机制。
- **⚡ 通用执行引擎**：抽象 `Executor` 接口，支持远程 SSH 实时流式执行与本地 Local 执行，具备超时熔断、退出码捕获与换行符自动清洗机制。
- **🛡️ 结构化备份编排**：统一管理多主机 restic 备份任务 (`backups.yaml`)，支持并发限制 (`--parallel N`)、安全 Dry-Run 模拟、自动初始化仓库与按保留策略自动修剪 (`forget --prune`)。
- **📊 实时日志流与本地落盘**：终端实时输出带服务器前缀标签的交互日志，并在 `$XDG_DATA_HOME/opspulse/logs/` 自动落盘保存。
- **💾 纯 Go 嵌入式 SQLite 存储**：集成无 CGO 依赖的 `modernc.org/sqlite`，支持嵌入式 SQL 自动迁移，记录结构化执行历史与指标。
- **💡 智能 Shell 动态补全**：内置支持 Bash、Zsh、Fish 与 PowerShell 自动补全。支持命令、参数、服务器名、模板名与备份任务名的实时动态 Tab 联想与描述展示。
- **🔒 默认安全原则**：私钥绝不离机，运行时按需注入敏感凭据，无任何外部遥测上报。

---

## 🚀 快速上手

### 1. 编译安装

```bash
git clone https://github.com/volcano6/opspulse.git
cd opspulse
make build

# 验证安装
./bin/opspulse version
```

### 2. 配置 Shell 自动补全（可选）

```bash
# Bash (Linux/WSL)
opspulse completion bash | sudo tee /etc/bash_completion.d/opspulse > /dev/null && source /etc/bash_completion.d/opspulse

# Zsh (Oh-My-Zsh / Starship)
echo 'source <(opspulse completion zsh 2>/dev/null)' >> ~/.zshrc && source ~/.zshrc
```

### 3. 添加并管理服务器

```bash
# 注册一台 VPS（默认自动扫描 ~/.ssh/id_ed25519 或 ~/.ssh/id_rsa 私钥）
./bin/opspulse server add vps-01 --host 198.51.100.1 --user root --tags prod,web --desc "主 Web 节点"

# 测试 SSH 连通性与延迟（支持按 Tab 键自动补全服务器名称）
./bin/opspulse server test vps-01

# 查看当前已配置的服务器列表
./bin/opspulse server list
```

### 4. 查看可用模板并初始化服务器

```bash
# 查看所有可用脚本模板
./bin/opspulse template list

# 模拟执行（Dry Run）：仅打印执行计划与脚本信息，不建立真实连接
./bin/opspulse bootstrap vps-01 -t base,security,docker --dry-run

# 正式执行初始化（支持按 Tab 自动补全服务器与 -t 模板列表）
./bin/opspulse bootstrap vps-01 -t base,security,docker
```

### 5. 统一备份管理 (Backup)

```bash
# 查看已配置的备份任务
./bin/opspulse backup list

# 模拟备份执行（支持按 Tab 自动补全任务名或 all）
./bin/opspulse backup run web-data --dry-run

# 执行备份（支持指定单/多任务或 all 全部执行，支持并发数设置）
./bin/opspulse backup run all --parallel 2

# 查看所有备份任务的最新一次执行状态与指标
./bin/opspulse backup status

# 查看特定任务的历史执行记录（支持按 Tab 自动补全任务名）
./bin/opspulse backup history web-data

# 查询远端 restic 仓库中的实际快照列表
./bin/opspulse backup snapshots web-data
```

---

## 📂 配置与数据目录规范

OpsPulse 严格遵循 [XDG Base Directory 规范](https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html)（也支持通过环境变量 `OPSPULSE_HOME` 统一覆盖）：

| 路径 | 用途说明 |
|------|---------|
| `$XDG_CONFIG_HOME/opspulse/servers.yaml` | 服务器清单配置文件 |
| `$XDG_CONFIG_HOME/opspulse/assets.yaml` | 结构化业务资产定义文件 |
| `$XDG_CONFIG_HOME/opspulse/backups.yaml` | 备份任务配置文件 |
| `$XDG_CONFIG_HOME/opspulse/templates/*.sh` | 用户自定义 Shell 脚本模板目录 |
| `$XDG_DATA_HOME/opspulse/logs/` | 任务执行完整日志落盘目录 (`bootstrap-<server>-<timestamp>.log`) |
| `$XDG_DATA_HOME/opspulse/opspulse.db` | 本地 SQLite 数据库文件（执行历史、状态与指标） |

---

## 🛠️ CLI 命令速查表

| 命令 | 说明 |
|------|------|
| `opspulse server add <name> --host <ip>` | 向清单中添加或更新服务器配置 |
| `opspulse server list` | 格式化表格列出所有已配置的服务器 |
| `opspulse server test <name>` | 测试与目标服务器的 SSH 连通性（支持 Tab 动态补全服务器名） |
| `opspulse server remove <name>` | 从清单中删除指定服务器（支持 Tab 动态补全服务器名） |
| `opspulse template list` | 列出所有内置及自定义脚本模板 |
| `opspulse template show <name>` | 查看指定模板的元数据与完整脚本内容（支持 Tab 动态补全模板名） |
| `opspulse bootstrap <servers...> -t <templates...>` | 串行执行服务器初始化任务（支持 Tab 动态补全服务器与模板） |
| `opspulse backup list` | 列出所有配置的备份任务 |
| `opspulse backup run <jobs... \| all> [-p N] [--dry-run]` | 执行备份任务（支持 Tab 动态补全任务名与 all） |
| `opspulse backup status` | 表格化展示所有任务的最新备份状态与数据指标 |
| `opspulse backup history <job-name>` | 查看指定任务的详细历史执行记录（支持 Tab 动态补全任务名） |
| `opspulse backup snapshots <job-name>` | 查询并列出远端仓库实际存储的快照列表（支持 Tab 动态补全任务名） |
| `opspulse completion <bash\|zsh\|fish\|powershell>` | 生成指定 Shell 的自动补全脚本 |
| `opspulse version` | 输出当前版本号、Git Commit Hash 与构建日期 |

---

## 📖 使用文档

* [新手入门教程](docs/tutorial/getting_started.md)
* [业务资产模型指南](docs/reference/asset.md)
* [备份管理指南](docs/reference/backup.md)
* [脚本模板开发指南](docs/reference/templates.md)
* [配置与存储目录规范](docs/reference/configuration.md)
* [贡献指南](CONTRIBUTING.md)
* [安全策略](SECURITY.md)

---

## 📄 开源协议

基于 [Apache License 2.0](LICENSE) 协议开源。
