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

- **🚀 服务器清单与标签管理**：使用简洁的 YAML (`servers.yaml`) 统一管理所有服务器，支持键值 Labels、标签、SSH 密钥认证、密码备选与自定义端口，支持 `--filter` 快速筛选。
- **🔍 Agentless 系统与硬件探测**：内置 `server info` 命令，单次 SSH 聚合采集 OS、Kernel、CPU 规格、内存/Swap 已用量、磁盘空间、开机时长、Docker 容器统计与 BBR 启用状态。
- **⚡ 原生交互式 SSH 直连**：`opspulse ssh <name>` 免记 IP/端口/密钥，自动桥接密码认证与原生密钥直连，100% 支持 vim/tmux/htop/resize。
- **🔑 自动化密钥配对注入**：`opspulse server setup-key <name>` 自动生成专用密钥并安全写入远端 `authorized_keys`，密码转私钥一键完成。
- **🧩 结构化业务资产 (Asset)**：支持 Docker Compose、Volume、数据库 Dump、Nginx 站点等有状态资产，以稳定全局 ID 标识，支持跨机灵活路径重映射（Remap）。
- **📜 脚本模板系统**：Shell 脚本支持 YAML Frontmatter 元数据头部。内置开箱即用的官方模板（`base`、`docker`、`security`、`restic`），支持自定义模板与同名优先覆盖机制。
- **🛡️ 结构化备份编排**：统一管理多主机 restic 备份任务 (`backups.yaml`)，支持并发限制 (`--parallel N`)、安全 Dry-Run 模拟、自动初始化仓库与按保留策略自动修剪 (`forget --prune`)。
- **📊 实时日志流与本地落盘**：终端实时输出带服务器前缀标签的交互日志，并在 `$XDG_DATA_HOME/opspulse/logs/` 自动落盘保存。
- **💾 纯 Go 嵌入式 SQLite 存储**：集成无 CGO 依赖的 `modernc.org/sqlite`，支持嵌入式 SQL 自动迁移，记录结构化执行历史与指标。
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

### 2. 添加并管理服务器 (Server Ops)

```bash
# 注册一台 VPS（支持指定 labels 键值对，默认自动扫描 ~/.ssh/id_ed25519 或 ~/.ssh/id_rsa）
./bin/opspulse server add oracle-sg --host 168.138.1.1 --user ubuntu --labels provider=oracle,region=sg --tags prod,web --desc "主 Web 节点"

# 查看当前已配置的服务器列表（支持按 label 或 tag 过滤）
./bin/opspulse server list --filter provider=oracle

# 快速探查目标服务器的系统、硬件规格与 Docker 状态
./bin/opspulse server info oracle-sg

# 使用已配置的密码自动认证并进入终端；绑定私钥时仅使用该密钥
./bin/opspulse ssh oracle-sg

# 将本地生成的专用密钥追加到远端 authorized_keys，并写回 key_path
# 远端密码及密码登录配置保持不变
./bin/opspulse server setup-key oracle-sg

# 远程执行单条命令（实时流式输出）
./bin/opspulse exec oracle-sg "docker ps"

# 通过 SFTP 极速上传/下载配置文件
./bin/opspulse upload oracle-sg ./nginx.conf /etc/nginx/nginx.conf
./bin/opspulse download oracle-sg /var/log/nginx/error.log ./error.log

# 测试 SSH 连通性与网络延迟
./bin/opspulse server test oracle-sg
```

### 3. 查看可用模板并初始化服务器 (Bootstrap)

```bash
# 查看所有可用脚本模板
./bin/opspulse template list

# 模拟执行（Dry Run）：仅打印执行计划与脚本信息，不建立真实连接
./bin/opspulse bootstrap oracle-sg -t base,security,docker --dry-run

# 正式执行初始化（支持按 Tab 自动补全服务器与 -t 模板列表）
./bin/opspulse bootstrap oracle-sg -t base,security,docker
```

### 4. 统一备份管理 (Backup)

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

### 5. 业务资产管理 (Asset)

```bash
# 注册业务资产（Docker Compose 项目、数据库、Nginx 配置等）
./bin/opspulse asset add blog-compose --type docker_compose --source /opt/blog --desc "Ghost 博客"
./bin/opspulse asset add blog-mysql --type database --source /var/lib/mysql --engine mysql --container blog-db

# 查看所有已配置的资产
./bin/opspulse asset list

# 查看资产详情
./bin/opspulse asset show blog-mysql

# 删除资产
./bin/opspulse asset remove blog-mysql
```

### 6. 精准还原与跨机迁移 (Restore)

```bash
# 全量还原最新快照到原始服务器
./bin/opspulse restore run web-data

# 精准还原单个资产
./bin/opspulse restore run web-data --asset blog-mysql

# 跨机迁移（还原到新 VPS，支持路径重映射）
./bin/opspulse restore run web-data --target-server new-vps --target-path /data/web

# Dry-Run 预览将还原的文件列表
./bin/opspulse restore run web-data --dry-run

# 查看还原历史
./bin/opspulse restore history web-data
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
| `opspulse server add <name> --host <ip> [--labels k=v]` | 向清单中添加或更新服务器配置 |
| `opspulse server list [--filter <key=val>]` | 格式化表格列出所有已配置的服务器（支持标签筛选） |
| `opspulse server set <name> [--host] [--port] [--key]` | 增量修改已有服务器配置字段 |
| `opspulse server edit <name>` | 用本地编辑器安全打开并编辑服务器配置 |
| `opspulse server setup-key <name>` | 自动为指定服务器生成并安装专用 SSH 密钥对 |
| `opspulse server info <name>` | 无侵入探测并输出服务器系统/硬件/Docker 运行状态看板 |
| `opspulse server test <name>` | 测试与目标服务器的 SSH 连通性与网络延迟 |
| `opspulse server remove <name>` | 从清单中删除指定服务器 |
| `opspulse ssh <name> [-- <args...>]` | 建立原生交互式 SSH 终端直连会话（支持参数透传） |
| `opspulse exec <name> <command...>` | 远程执行单条 Shell 命令并实时返回输出与退出码 |
| `opspulse upload <name> <src> <dst> [-r]` | 通过 SFTP 将本地文件或目录递归上传至远程服务器 |
| `opspulse download <name> <src> <dst> [-r]` | 通过 SFTP 将远程文件或目录递归下载至本地 |
| `opspulse template list` | 列出所有内置及自定义脚本模板 |
| `opspulse template show <name>` | 查看指定模板的元数据与完整脚本内容 |
| `opspulse bootstrap <servers...> -t <templates...>` | 串行执行服务器初始化任务 |
| `opspulse backup list` | 列出所有配置的备份任务 |
| `opspulse backup run <jobs... \| all> [-p N] [--dry-run]` | 执行备份任务（支持并发与模拟执行） |
| `opspulse backup status` | 表格化展示所有任务的最新备份状态与数据指标 |
| `opspulse backup history <job-name>` | 查看指定任务的详细历史执行记录 |
| `opspulse backup snapshots <job-name>` | 查询并列出远端仓库实际存储的快照列表 |
| `opspulse asset add <id> --type <type> --source <path>` | 注册或更新有状态业务资产 |
| `opspulse asset list` | 格式化表格列出所有已配置的资产 |
| `opspulse asset show <id>` | 查看指定资产的详细配置信息 |
| `opspulse asset remove <id>` | 从配置中删除指定资产 |
| `opspulse restore run <job> [--snapshot id] [--asset id]` | 从 restic 快照执行还原（支持跨机迁移与精准资产还原） |
| `opspulse restore history [job-name]` | 查看还原操作的历史执行记录 |
| `opspulse completion <bash\|zsh\|fish\|powershell>` | 生成指定 Shell 的自动补全脚本 |
| `opspulse version` | 输出当前版本号、Git Commit Hash 与构建日期 |

---

## 📖 使用文档

* [新手入门教程](docs/tutorial/getting_started.md)
* [日常服务器管理指南](docs/reference/server_ops.md)
* [业务资产模型指南](docs/reference/asset.md)
* [备份管理指南](docs/reference/backup.md)
* [还原管理指南](docs/reference/restore.md)
* [脚本模板开发指南](docs/reference/templates.md)
* [配置与存储目录规范](docs/reference/configuration.md)
* [贡献指南](CONTRIBUTING.md)
* [安全策略](SECURITY.md)

---

## 📄 开源协议

基于 [Apache License 2.0](LICENSE) 协议开源。
