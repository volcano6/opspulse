# OpsPulse

[![CI](https://github.com/volcano6/opspulse/actions/workflows/ci.yaml/badge.svg)](https://github.com/volcano6/opspulse/actions/workflows/ci.yaml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/volcano6/opspulse)](https://go.dev/)
[![License](https://img.shields.io/github/license/volcano6/opspulse)](LICENSE)

**Infrastructure Action Runner** — 面向个人开发者的自托管服务器自动化与备份编排工具。

---

## 🎯 为什么需要 OpsPulse？

管理多台云服务器（VPS）时，个人开发者经常遇到以下痛点：

* 🔄 **重复初始化配置**：每次开通新 VPS 都要手动敲一遍重复的 curl、apt、常用工具和安全配置。
* 📦 **备份脚本散落各处**：各台服务器上的备份脚本和 cron 任务缺乏统一监控，备份成功与否无感知。
* 🚚 **机器到期迁移痛苦**：VPS 到期更换服务商时，数据导出、环境安装、路径调整、证书配置和重新上线流程繁琐且易出错。
* 🔒 **敏感凭据散落**：各种密码和 API Token 散落在各个服务器的明文 `.env` 文件或记忆中。

**OpsPulse** 采用单一可执行二进制文件，提供声明式服务器清单、有状态业务资产（Asset）管理、可复用的 Shell 模板、SFTP 文件传输、统一调度的 restic 备份与跨机重映射还原，并将所有执行历史与状态结构化持久化至本地 SQLite 数据库。

---

## ✨ 核心特性

- **🚀 服务器清单与标签管理**：使用简洁的 YAML (`servers.yaml`) 统一管理所有服务器，支持键值 Labels、标签、SSH 密钥认证、密码备选与自定义端口，支持 `--filter` 快速筛选。
- **🔍 Agentless 系统与硬件探测**：内置 `server info` 命令，单次 SSH 聚合采集 OS、Kernel、CPU 规格、内存/Swap 已用量、磁盘空间、开机时长、Docker 容器统计与 BBR 启用状态。
- **⚡ 原生交互式 SSH 直连**：`ops ssh <name>` 免记 IP/端口/密钥，自动桥接密码认证与原生密钥直连，100% 支持 vim/tmux/htop/resize。
- **🔑 自动化密钥配对注入**：`ops server setup-key <name>` 自动生成专用密钥并安全写入远端 `authorized_keys`，密码转私钥一键完成。
- **🧩 结构化业务资产 (Asset)**：支持 Docker Compose、Volume、数据库 Dump、Nginx 站点等有状态资产，以稳定全局 ID 标识，支持跨机灵活路径重映射（Remap）。
- **📜 脚本模板系统**：Shell 脚本支持 YAML Frontmatter 元数据头部。内置开箱即用的官方模板（`base`、`docker`、`security`、`restic`），支持自定义模板与同名优先覆盖机制。
- **🛡️ 结构化备份编排**：统一管理多主机 restic 备份任务 (`backups.yaml`)，支持并发限制 (`--parallel N`)、安全 Dry-Run 模拟、自动初始化仓库与按保留策略自动修剪 (`forget --prune`)。
- **🐳 容器智能备份与跨机快起**：无需预先编写 YAML，直接 `ops backup run <server>:<container> [--as <name>]`。野生容器自动逆向转译为标准 `compose.yaml`，MySQL/PostgreSQL 自动执行容器内在途热 Dump 与 gzip 即时压缩，跨机还原 `ops restore run <name> --target-server <vps>` 默认自动自适应拉起容器并自动灌库。
- **⏰ 定时调度与自动化守护**：支持标准 Cron 表达式（`@daily`、`@hourly` 等），内置防重叠并发保护与优雅退出，通过 `ops daemon` 长期驻留或 `--once` 单次批量触发。
- **🔔 Webhook 告警通知**：任务执行完毕或出现故障时自动触发，开箱即用兼容 Slack、Discord、企业微信、钉钉、飞书与通用 Webhook，支持仅在失败时精准告警。
- **📊 实时日志流与本地落盘**：终端实时输出带服务器前缀标签的交互日志，并在 `$XDG_DATA_HOME/opspulse/logs/` 自动落盘保存。
- **💾 纯 Go 嵌入式 SQLite 存储**：集成无 CGO 依赖的 `modernc.org/sqlite`，支持嵌入式 SQL 自动迁移，记录结构化执行历史与指标。
- **🔒 零信任凭证流转与安全边界**：支持在 `backups.yaml` 中使用 `op://` 协议，运行时自动调用 1Password 解析凭证、不落盘；`ops 1p push/pull/status` 可将 SSH 私钥整体托管至 1Password 并以 `op://` 引用按需取用，本地无需保留私钥文件；集成 `SSH Agent` 自适应探测；支持 WSL 到 Windows 的原生私钥智能安全桥接。默认强制启用严格主机密钥校验（Strict Host Key Checking，未知主机输出密钥类型与 SHA256 指纹提示阻断中间人攻击），支持 `OPSPULSE_TRUST_NEW_HOST_KEY=1` 显式声明首次连接自动受信（等价于 `accept-new` 并通过日志/终端线程安全告警），全模式严密阻断任何主机密钥不匹配与篡改。私钥绝不主动离机，无任何外部遥测上报。

---

## 🚀 快速上手

### 1. 编译安装与自动补全

```bash
git clone https://github.com/volcano6/opspulse.git
cd opspulse
make install

# 一键将补全脚本与 PATH 配置注入当前 Shell profile（支持 Bash / Zsh / Fish / PowerShell）
# 若当前终端尚未生效 PATH，可直接执行本地构建产物完成初始化：
./bin/ops completion --install
source ~/.zshrc  # 或 source ~/.bashrc

# 验证安装
ops version
```

### 2. 添加并管理服务器 (Server Ops)

```bash
# 注册一台 VPS（极简语法：ops add <name> [user@]host[:port]，缺省密码静默交互输入，连通后可一键注入公钥免密直连）
ops add oracle-sg ubuntu@168.138.1.1 --labels provider=oracle,region=sg --tags prod,web --desc "主 Web 节点"

# 查看当前已配置的服务器列表（支持极简别名 ops ls，支持按 label 或 tag 过滤）
ops ls --filter provider=oracle

# 快速探查目标服务器的系统、硬件规格与 Docker 状态
ops server info oracle-sg

# 终端极速直连（无参执行 ops ssh 弹出交互菜单直选；亦可指定服务器名直接进入）
ops ssh oracle-sg

# 一键导出并幂等写入 ~/.ssh/config（打通 VS Code Remote-SSH / Cursor / 系统原生 ssh）
ops export ssh-config --write

# 将本地生成的专用密钥追加到远端 authorized_keys，并写回 key_path
# 远端密码及密码登录配置保持不变
ops server setup-key oracle-sg

# 远程执行单条命令（实时流式输出，支持免引号参数透传）
ops exec oracle-sg docker ps

# 通过 SFTP 统一双向快速传输文件或目录
ops cp ./nginx.conf oracle-sg:/etc/nginx/nginx.conf

# 自动唤起本地外部 GUI SFTP 客户端（自动检测 WinSCP / Xftp / FileZilla / Cyberduck）
ops sftp oracle-sg
# 亦可指定客户端或特定目录
ops sftp oracle-sg --app xftp --path /var/log
ops cp oracle-sg:/var/log/nginx/error.log ./error.log
ops cp -r ./configs oracle-sg:/opt/app/configs

# 测试 SSH 连通性与网络延迟
ops server test oracle-sg
```

### 3. 查看可用模板并初始化服务器 (Bootstrap)

```bash
# 查看所有可用脚本模板
ops template list

# 模拟执行（Dry Run）：仅打印执行计划与脚本信息，不建立真实连接
ops bootstrap oracle-sg -t base,security,docker --dry-run

# 正式执行初始化（支持按 Tab 自动补全服务器与 -t 模板列表）
ops bootstrap oracle-sg -t base,security,docker
```

### 4. 统一备份管理 (Backup)

```bash
# 查看已配置的备份任务
ops backup list

# 模拟备份执行（支持按 Tab 自动补全任务名或 all）
ops backup run web-data --dry-run

# 执行备份（支持指定单/多任务或 all 全部执行，支持并发数设置）
ops backup run all --parallel 2

# 一键备份野生容器（自动逆向转译 Compose 与数据库热导）
ops backup run oracle-sg:my-nginx --as web-nginx

# 查看所有备份任务的最新一次执行状态与指标
ops backup status

# 查看特定任务的历史执行记录（支持按 Tab 自动补全任务名）
ops backup history web-data

# 查询远端 restic 仓库中的实际快照列表
ops backup snapshots web-data
```

### 5. 业务资产管理 (Asset)

```bash
# 注册业务资产（Docker Compose 项目、数据库、Nginx 配置等）
ops asset add blog-compose --type docker_compose --source /opt/blog --desc "Ghost 博客"
ops asset add blog-mysql --type database --source /var/lib/mysql --engine mysql --container blog-db

# 查看所有已配置的资产
ops asset list

# 查看资产详情
ops asset show blog-mysql

# 删除资产
ops asset remove blog-mysql
```

### 6. 精准还原与跨机迁移 (Restore)

```bash
# 全量还原最新快照到原始服务器
ops restore run web-data

# 精准还原单个资产
ops restore run web-data --asset blog-mysql

# 跨机迁移（还原到新 VPS，默认自动自适应拉起容器并灌库）
ops restore run web-data --target-server new-vps --target-path /data/web

# Dry-Run 预览将还原的文件列表
ops restore run web-data --dry-run

# 查看还原历史
ops restore history web-data
```

### 7. 定时调度与自动化守护 (Scheduler)

```bash
# 启动调度守护进程（前台运行，按 backups.yaml 中的 schedule 自动执行备份并触发告警）
ops daemon

# 单次按序执行全部已调度任务后退出（适配外部 crontab 或 systemd timer）
ops daemon --once
```

### 8. 告警通知与连通性自测 (Notifications)

```bash
# 查看所有已配置的通知渠道
ops notify list

# 发送测试消息验证指定 Webhook 渠道的连通性
ops notify test slack-ops

# 验证所有配置渠道
ops notify test
```

---

## 📂 配置与数据目录规范

OpsPulse 严格遵循 [XDG Base Directory 规范](https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html)（也支持通过环境变量 `OPSPULSE_HOME` 统一覆盖）：

| 路径 | 用途说明 |
|------|---------|
| `$XDG_CONFIG_HOME/opspulse/servers.yaml` | 服务器清单配置文件 |
| `$XDG_CONFIG_HOME/opspulse/assets.yaml` | 结构化业务资产定义文件 |
| `$XDG_CONFIG_HOME/opspulse/backups.yaml` | 备份任务配置文件，可能包含明文凭据，权限为 `0600` |
| `$XDG_CONFIG_HOME/opspulse/notifications.yaml` | Webhook 告警渠道配置文件 |
| `$XDG_CONFIG_HOME/opspulse/templates/*.sh` | 用户自定义 Shell 脚本模板目录 |
| `$XDG_DATA_HOME/opspulse/logs/` | 任务执行完整日志落盘目录 (`bootstrap-<server>-<timestamp>.log`) |
| `$XDG_DATA_HOME/opspulse/opspulse.db` | 本地 SQLite 数据库文件（执行历史、状态与指标） |

---

## 🛠️ CLI 命令速查表

| 命令 | 说明 |
|------|------|
| `ops ls [--filter <key=val>]` | 极速查看所有已配置服务器（`ops server list` 顶级直达，支持标签筛选） |
| `ops cp <src> <dst> [-r]` | 统一双向 SFTP 文件/目录传输（智能识别 `[server:]path` 远程前缀） |
| `ops add <name> [target] [-i key]` | 向清单中添加或更新服务器（支持 `[user@]host[:port]`，缺省静默交互输入密码并引导注入公钥） |
| `ops server list [--filter <key=val>]` | 格式化表格列出所有已配置的服务器 |
| `ops server set <name> [--host] [--port] [--key]` | 增量修改已有服务器配置字段 |
| `ops server edit <name>` | 用本地编辑器安全打开并编辑服务器配置 |
| `ops server setup-key <name>` | 自动为指定服务器生成并安装专用 SSH 密钥对 |
| `ops server info <name>` | 无侵入探测并输出服务器系统/硬件/Docker 运行状态看板 |
| `ops server test <name>` | 测试与目标服务器的 SSH 连通性与网络延迟 |
| `ops server remove <name>` | 从清单中删除指定服务器 |
| `ops 1p push <server>... [--all] [--vault <vault>] [--delete-local]` | 把本地私钥推送到 1Password，并把服务器改绑为 `op://` 引用（保险库只有一个时自动选中） |
| `ops 1p pull <server>` | 把 1Password 中的私钥取回本地 `~/.ssh/` 并重新绑定 |
| `ops 1p status [--filter <key=val>]` | 查看每台服务器的私钥当前存放在哪里 |
| `ops 1p config [--vault <v>] [--account <a>] [--unset]` | 查看/记住默认保险库与账号，之后 `push` 无需重复传参 |
| `ops ssh [name] [-- <args...>]` | 原生交互式 SSH 终端会话（无参时弹出菜单交互直选） |
| `ops sftp [server] [--app <app>] [--path <path>] [--cli]` | 自动唤起外部 GUI SFTP 客户端（WinSCP/Xftp/FileZilla）或 CLI 管理远端文件 |
| `ops export ssh-config [--write]` | 导出 OpenSSH 配置，打通 VS Code / Cursor / 系统终端（`--write` 幂等写入 `~/.ssh/config`） |
| `ops exec <name> <command...>` | 远程执行单条 Shell 命令并实时返回输出与退出码（支持免引号透传） |
| `ops template list` | 列出所有内置及自定义脚本模板 |
| `ops template show <name>` | 查看指定模板的元数据与完整脚本内容 |
| `ops bootstrap <servers...> -t <templates...>` | 串行执行服务器初始化任务 |
| `ops backup list` | 列出所有配置的备份任务 |
| `ops backup run <jobs... \| all \| srv:ctr> [--as name]` | 执行备份任务或一键智能备份容器 |
| `ops backup status` | 表格化展示所有任务的最新备份状态与数据指标 |
| `ops backup history <job-name>` | 查看指定任务的详细历史执行记录 |
| `ops backup snapshots <job-name>` | 查询并列出远端仓库实际存储的快照列表 |
| `ops asset add <id> --type <type> --source <path>` | 注册或更新有状态业务资产 |
| `ops asset list` | 格式化表格列出所有已配置的资产 |
| `ops asset show <id>` | 查看指定资产的详细配置信息 |
| `ops asset remove <id>` | 从配置中删除指定资产 |
| `ops restore run <job> [--target-server vps] [--as name] [--no-start]` | 从快照执行还原（默认自动跨机拉起容器并灌库） |
| `ops restore history [job-name]` | 查看还原操作的历史执行记录 |
| `ops daemon [--once]` | 运行定时调度守护进程自动执行备份（支持单次批量模式） |
| `ops notify list` | 查看所有配置的 Webhook 告警渠道 |
| `ops notify test [channel-name]` | 发送测试事件验证通知渠道的连通性 |
| `ops completion [shell] [--install]` | 生成自动补全脚本或一键自动安装至 Shell Profile |
| `ops version` | 输出当前版本号、Git Commit Hash 与构建日期 |

---

## 📖 使用文档

* [新手入门教程](docs/tutorial/getting_started.md)
* [跨端环境备份与无损还原指南 (WSL/1Password)](docs/tutorial/wsl_env_backup.md)
* [容器备份与跨机无缝迁移实战指南](docs/tutorial/container_migration.md)
* [日常服务器管理指南](docs/reference/server_ops.md)
* [1Password 私钥托管指南](docs/reference/onepassword.md)
* [业务资产模型指南](docs/reference/asset.md)
* [备份管理指南](docs/reference/backup.md)
* [还原管理指南](docs/reference/restore.md)
* [定时调度指南](docs/reference/scheduler.md)
* [告警通知指南](docs/reference/notifications.md)
* [脚本模板开发指南](docs/reference/templates.md)
* [配置与存储目录规范](docs/reference/configuration.md)
* [贡献指南](CONTRIBUTING.md)
* [安全策略](SECURITY.md)

---

## 📄 开源协议

基于 [Apache License 2.0](LICENSE) 协议开源。
