# 配置与模板 (Configuration and Templates)

OpsPulse 的全部本机状态都落在 XDG 目录下的几个 YAML 文件与脚本模板里：`servers.yaml` 记服务器清单、
`assets.yaml` 记业务资产、`notifications.yaml` 记告警渠道、`templates/*.sh` 记脚本模板，运行数据落在本地
SQLite。本文覆盖这些目录、配置文件字段与模板规范；命令与 flag 的完整清单见[命令行参考](cli.md)。

---

## 1. 目录结构 (Directory Layout)

OpsPulse 严格按照各操作系统的 XDG Base Directory 规范组织配置文件与运行时数据目录。

| 目录类型 | Linux 默认路径 | macOS 默认路径 | Windows 默认路径 | 环境变量覆盖 |
|---------|---------------|---------------|-----------------|-------------|
| **配置目录** | `~/.config/opspulse/` | `~/Library/Application Support/opspulse/` | `%APPDATA%\opspulse\` | `$OPSPULSE_HOME` / `$XDG_CONFIG_HOME` |
| **数据目录** | `~/.local/share/opspulse/` | `~/Library/Application Support/opspulse/` | `%LOCALAPPDATA%\opspulse\` | `$OPSPULSE_HOME/data` / `$XDG_DATA_HOME` |

macOS 上 `xdg.ConfigHome` 与 `xdg.DataHome` 都指向 `~/Library/Application Support`，
所以配置目录与数据目录默认是**同一个**路径。只有用 `$OPSPULSE_HOME` 覆盖时，数据目录才会
落在它下面的 `data/` 子目录里。

### 核心文件清单

| 文件路径 | 用途说明 |
|---------|---------|
| `$XDG_CONFIG_HOME/opspulse/servers.yaml` | 服务器清单；可能包含明文 SSH 密码，文件权限为 `0600` |
| `$XDG_CONFIG_HOME/opspulse/assets.yaml` | 结构化业务资产定义文件（可通过 `ops asset` 注册管理） |
| `$XDG_CONFIG_HOME/opspulse/backups.yaml` | 备份任务；`env` 中的凭据以明文保存，文件权限为 `0600` |
| `$XDG_CONFIG_HOME/opspulse/notifications.yaml` | 告警通知渠道（`ops notify`）；webhook URL 中通常带 token，文件权限为 `0600` |
| `$XDG_CONFIG_HOME/opspulse/onepassword.yaml` | 记住的 1Password 默认保险库与账号，供后续 `ops 1p` 免去 `--vault` / `--account` |
| `$XDG_CONFIG_HOME/opspulse/templates/*.sh` | 自定义 Shell 脚本模板目录 |
| `$XDG_DATA_HOME/opspulse/logs/` | 执行日志文件落盘目录 |
| `$XDG_DATA_HOME/opspulse/opspulse.db` | 本地 SQLite 数据库文件 |

这些文件的权限位与敏感级别，以及「为什么凭据必须留在本机磁盘」，见
[架构与信任模型](../explanation/architecture.md)；`servers.yaml` 里的凭据如何上传到 1Password、
又怎么还原回本地，见 [1Password 备份与跨机同步指南](onepassword.md)。

---

## 2. 服务器清单 (`servers.yaml`)

路径：`$XDG_CONFIG_HOME/opspulse/servers.yaml`

```yaml
servers:
  - name: web-01
    host: 198.51.100.10
    port: 22
    user: root
    key_path: ~/.ssh/id_ed25519
    tags:
      - prod
      - web
    labels:
      provider: oracle
      region: singapore
      purpose: blog
    description: 生产环境主 Web 节点

  - name: db-01
    host: 198.51.100.20
    port: 2222
    user: admin
    tags:
      - prod
      - database
    labels:
      provider: hetzner
      region: falkenstein
    description: 主 PostgreSQL 数据库节点
```

### 字段说明

| 字段 | 类型 | 必填 | 默认值 | 详细说明 |
|------|------|------|--------|---------|
| `name` | 字符串 | **是** | - | 服务器唯一标识名 |
| `host` | 字符串 | **是** | - | IP 地址或域名 |
| `port` | 整数 | 否 | `22` | SSH 端口号 |
| `user` | 字符串 | 否 | `root` | SSH 登录用户名 |
| `key_path` | 字符串 | 否 | `""` | 私钥文件路径（支持 `~` 自动展开）。配置后 OpenSSH 强制启用 `IdentitiesOnly=yes`，只提交该密钥；若密钥和密码均为空，则自动扫描默认密钥 |
| `password` | 字符串 | 否 | `""` | SSH 密码，以**明文**保存在权限为 `0600` 的 `servers.yaml` 中。未绑定私钥时用于自动认证；`server setup-key` 使用它安装公钥但不修改远端密码（加 `--remove-password` 可在验证新密钥可用后清除它）。运行时不支持 `op://` 引用：残留的引用会快速失败，需先跑 `ops 1p restore` 迁移为本地凭据 |
| `tags` | 字符串列表 | 否 | `[]` | 标签分组列表（便于按标签批量执行；包含 `legacy-ssh` / `legacy_ssh` / `legacy-rsa` 时为该主机受控开启老旧算法向下兼容） |
| `labels` | 键值映射 | 否 | `{}` | 结构化元数据标签（如 `provider: oracle`, `region: sg`；配置 `legacy-ssh: "true"` 或 `legacy-rsa: "true"` 亦可开启老旧算法向下兼容），支持 `ops ls --filter` 筛选 |
| `description` | 字符串 | 否 | `""` | 备注描述信息 |

---

## 3. 业务资产定义 (`assets.yaml`)

路径：`$XDG_CONFIG_HOME/opspulse/assets.yaml`

Asset 描述服务器上的有状态数据资产，每个资产拥有**稳定的唯一 ID**，跨机迁移或还原时通过 ID 引用并支持路径重映射（Remap）。可通过 `ops asset` 命令行子命令进行增删查改。

### 字段规范

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `id` | 字符串 | **是** | 资产唯一标识，仅允许字母、数字、连字符与下划线 |
| `type` | 字符串 | **是** | 资产类型标识 |
| `source` | 字符串 | **是** | 资产来源路径 |
| `engine` | 字符串 | 否 | 数据库引擎（`database` 类型使用，如 `mysql` / `postgres`） |
| `container` | 字符串 | 否 | 关联的容器名（`database` 类型使用） |
| `excludes` | 字符串列表 | 否 | 备份时排除的路径列表（`directory` 类型使用） |
| `description` | 字符串 | 否 | 备注描述信息 |

资产类型、完整示例与 `ops asset` 命令见 [服务器与远端操作](server_ops.md#8-业务资产与引用保护-assets)。

---

## 4. 通知渠道 (`notifications.yaml`)

路径：`$XDG_CONFIG_HOME/opspulse/notifications.yaml`

OpsPulse 提供开箱即用的自动化告警分发机制。在备份任务调度执行完毕或失败时，通知子系统会自动解析状态，并通过 Webhook 实时推送到用户配置的通知渠道。

```yaml
channels:
  # Slack 告警（仅在任务失败时通知）
  - name: slack-ops
    type: webhook
    url: "https://hooks.slack.com/services/YOUR/SLACK/WEBHOOK_URL"
    on: failure

  # Discord 运维群（仅在任务失败时通知）
  - name: discord-alerts
    type: webhook
    url: "https://discord.com/api/webhooks/YOUR/DISCORD/WEBHOOK_URL"
    on: failure

  # 飞书 / 钉钉 / 企业微信（支持所有状态）
  - name: feishu-bot
    type: webhook
    url: "https://open.feishu.cn/open-apis/bot/v2/hook/YOUR-BOT-TOKEN"
    on: always

  # 自定义内部运维监控平台
  - name: internal-webhook
    type: webhook
    url: "https://monitor.example.com/api/v1/opspulse/webhook"
    on: always
```

### 字段规范

| 字段 | 类型 | 是否必填 | 说明 |
|:---|:---|:---|:---|
| `name` | 字符串 | **是** | 渠道唯一名称标识（如 `slack-ops`、`discord-alerts`） |
| `type` | 字符串 | **是** | 渠道类型，当前支持 `webhook` |
| `url` | 字符串 | **是** | 接收通知的完整 HTTP / HTTPS 目标地址 |
| `on` | 字符串 | 否 | 触发过滤条件，可选 `failure`（默认）、`success`、`always` |

### 触发条件 (`on`) 行为说明

- `failure`（默认）：备份任务状态为 `failed` 或 `partial` 时触发通知。`partial` 指主体工作已完成、但后续步骤失败（例如还原后容器自动启动失败），对失败告警渠道而言同样算失败。**推荐日常使用，避免正常任务消息刷屏**。
- `success`：仅当备份任务成功完成时触发通知。
- `always`：无论成功还是失败均触发通知。

通知由备份任务的结束事件驱动：`ops daemon` 调度执行的任务在跑完后自动收集状态与快照信息并分发，
调度时机与并发保护见[备份、还原与调度指南 § 调度与守护进程](backup.md#4-调度与守护进程-scheduler)；
`partial` 为什么按失败处理、以及通知里刻意不带什么，见[架构与信任模型](../explanation/architecture.md)。

### CLI 管理与连通性自测

#### 查看所有已配置的通知渠道

```bash
ops notify list
```

输出示例：
```text
NAME            TYPE       TRIGGER   URL
----            ----       -------   ---
slack-ops       webhook    failure   https://hooks.slack.com/services/T0000000/B0000000/XXXXXXXXXXXXXXXXXXXXXXXX
discord-alerts  webhook    failure   https://discord.com/api/webhooks/000000000000000000/XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
feishu-bot      webhook    always    https://open.feishu.cn/open-apis/bot/v2/hook/00000000-0000-0000-0000-000000000000
```

> [!WARNING]
> 上表里的 URL 是**原样打印的完整 URL**——webhook 的鉴权 token 就在 path 里，
> 换句话说 `ops notify list` 会把凭据显示在终端上。请只在本地终端查看，
> 不要把它重定向到共享文件、粘进 Issue/PR，或让它出现在 CI 日志里。
> （当前实现不做打码；需要脱敏时请自行截断输出。）

#### 测试通知投递

在正式启用定时备份前，可以通过 `notify test` 验证 Webhook 是否能正常收到测试卡片：

```bash
# 测试指定渠道
ops notify test slack-ops

# 测试全部已配置的渠道
ops notify test
```

---

## 5. 告警请求体结构 (Webhook Payload)

OpsPulse 发送标准 HTTP POST 请求，Header 包含：
- `Content-Type: application/json; charset=utf-8`
- `User-Agent: OpsPulse-Notifier/1.0`

### JSON 消息体

```json
{
  "event": "backup_failed",
  "job_name": "web-data",
  "status": "failed",
  "server": "prod-vps",
  "snapshot": "",
  "duration_seconds": 12.45,
  "error": "failed to connect to host: dial tcp 198.51.100.1:22: i/o timeout",
  "timestamp": "2026-09-03T19:30:00Z",
  "text": "[OpsPulse] ❌ Backup job \"web-data\" on \"prod-vps\" FAILED: failed to connect to host (duration: 12.45s)",
  "content": "[OpsPulse] ❌ Backup job \"web-data\" on \"prod-vps\" FAILED: failed to connect to host (duration: 12.45s)"
}
```

> [!TIP]
> 消息体中内置了 `text`（适配 Slack Webhook）和 `content`（适配 Discord Webhook）字段，各主流 IM 平台的 Incoming Webhook 无需额外中间层转发即可直接展示格式化卡片。

---

## 6. 模板与 Frontmatter (Templates)

OpsPulse 使用声明式 Shell 脚本模板来执行服务器初始化、日常维护及自动化任务。

### 内置模板清单

OpsPulse 直接通过 `go:embed` 将以下经过充分验证的官方模板嵌入到二进制中：

| 模板名称 | 操作系统支持 | 功能描述 | 主要执行动作 |
|----------|------------|---------|-------------|
| `cn` | Ubuntu, Debian | 国内 VPS 网络与镜像加速 | 自动切换 APT 国内源（内网/公网自适应）、Git GitHub 全局代理、Docker 镜像源（默认仅公共源，可用 `-t cn:<镜像地址>` 追加自建源）、Go/Pip 生态源 |
| `base` | Ubuntu, Debian | 系统基础工具集 | 自动更新 apt 缓存，安装常用工具与排障套件，开启 TCP BBR 拥塞控制 |
| `bbr` | Ubuntu, Debian | 开启 TCP BBR 拥塞控制 | 独立开启 Linux TCP BBR 与 fq 排队规则（写入 `/etc/sysctl.d/99-bbr.conf`） |
| `security` | Ubuntu, Debian | 安全与防火墙加固 | 智能识别当前活跃 SSH 端口并自动放行，放行 Web 80/443，开启 UFW 与 fail2ban 防暴破 |
| `firewall-ports` | Ubuntu, Debian | 开放自定义防火墙端口 | 按参数灵活批量放行端口（如 `-t firewall-ports:80,443,8080/tcp,51820/udp`） |
| `docker` | Ubuntu, Debian | Docker CE 容器环境 | 安装 Docker CE 与 Compose 插件，支持国内镜像源自动回退与 daemon.json 日志轮转配置 |
| `nginx` | Ubuntu, Debian | Nginx Web 服务器 | 配置官方源安装最新稳定版 Nginx，开机自启并放行 80/443 端口 |
| `caddy` | Ubuntu, Debian | Caddy Web 服务器 | 安装官方 Caddy 并设置开机自启，自动申请 HTTPS 证书 |
| `golang` | Ubuntu, Debian | Go 语言开发环境 | 从官方/国内镜像下载安装指定或最新稳定版 Go，自动配置 PATH 与软链接 |
| `nodejs` | Ubuntu, Debian | Node.js 开发环境与工具链 | 配置 NodeSource 源安装 Node.js LTS（默认 24，支持传参如 `-t nodejs:22` 或 `nodejs:20`），安装 npm 并启用 corepack |
| `uv` | Ubuntu, Debian | Astral uv Python 工具链 | 安装极速 Python 包与项目管理工具 uv/uvx 至 `/usr/local/bin` |
| `restic` | Ubuntu, Debian | 备份工具链 | 安装 `restic` 与 `rclone` 二进制包，为 `ops backup` 提供执行基础 |
| `swap` | Ubuntu, Debian | 零停机 Swap 扩容/调整 | 默认创建 2GB（可传参调整，如 `-t swap:4`），双文件热切换，优化 swappiness |
| `timezone` | Ubuntu, Debian | 系统时区与时间同步 | 默认设置 `Asia/Shanghai`（支持传参如 `-t timezone:UTC`），开启 NTP 自动授时 |
| `tmux` | Ubuntu, Debian | 终端复用与精巧配置 | 安装 tmux，配置鼠标滚动支持、10000 行历史回滚与 Dracula 主题状态栏 |
| `zsh-starship` | Ubuntu, Debian | 现代终端与美化 | 安装 Zsh + Starship 提示符，内置 Node/Go/Rust 工具链自探测与高亮补全插件（支持传参 `-t zsh-starship:force` 强制刷新提取） |
| `clean` | Ubuntu, Debian | 磁盘与资源清理 | 清理 apt 缓存、7天前 journalctl 日志与无用 Docker 资源 |
| `upgrade` | Ubuntu, Debian | 系统包安全更新 | 无人值守升级系统软件包与安全补丁，检测内核更新并提示重启 |
| `cluster-check` | Ubuntu, Debian | 节点指标快捷巡检 | 单框直观输出节点主机名、IP、负载、内存使用与根分区磁盘空间 |

模板只描述「装什么」，挑哪台机器、按什么顺序执行归 `ops bootstrap`：端到端用法见
[新手入门教程](../tutorial/getting_started.md)，`ops template` 与 `ops bootstrap` 的参数清单见
[命令行参考](cli.md)。

### YAML Frontmatter 元数据语法

每个脚本模板可在文件头部通过 `# ---` 区块定义可选的元数据声明：

```bash
#!/bin/bash
# ---
# name: nodejs-setup
# version: 1
# os: [ubuntu, debian]
# description: 通过 NodeSource 源安装 Node.js 24 LTS
# ---
set -euo pipefail

echo "=== 安装 Node.js LTS ==="
curl -fsSL https://deb.nodesource.com/setup_24.x | bash -
apt-get install -y nodejs
node -v
npm -v
```

### Frontmatter 字段说明

| 字段 | 类型 | 是否必填 | 说明 |
|------|------|----------|------|
| `name` | 字符串 | 否 | 模板唯一标识名。若未填写，默认使用去除 `.sh` 后的文件名。 |
| `version` | 整数 | 否 | 模板版本号（默认为 `1`）。 |
| `os` | 字符串列表 | 否 | 支持的目标操作系统列表（例如 `[ubuntu, debian]`）。 |
| `description` | 字符串 | 否 | 模板功能简介，展示在 `ops template list` 中。 |

### 自定义脚本模板

你可以将自己的 `.sh` 脚本放置在用户自定义模板目录下：

```bash
# 默认自定义模板路径：
# Linux:   ~/.config/opspulse/templates/
# macOS:   ~/Library/Application Support/opspulse/templates/
# Windows: %APPDATA%/opspulse/templates/

mkdir -p ~/.config/opspulse/templates

cat <<'EOF' > ~/.config/opspulse/templates/caddy.sh
#!/bin/bash
# ---
# name: caddy
# version: 1
# os: [ubuntu, debian]
# description: 安装并配置 Caddy 现代 Web 服务器
# ---
apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | tee /etc/apt/sources.list.d/caddy-stable.list
apt-get update
apt-get install -y caddy
caddy version
EOF
```

运行查看命令验证自定义模板是否已被正确识别：
```bash
ops template list
```

### 同名优先覆盖机制

如果自定义目录中存在与内置模板同名的脚本（如 `~/.config/opspulse/templates/docker.sh`），**OpsPulse 将优先使用用户自定义的模板**，方便用户针对个人特殊需求对官方模板进行覆写。

---

## 7. SQLite 数据库 (`opspulse.db`)

路径：`$XDG_DATA_HOME/opspulse/opspulse.db`

OpsPulse 使用纯 Go 实现的 `modernc.org/sqlite` 驱动管理状态与历史指标：

* **WAL 模式**：默认开启 Write-Ahead Logging，支持并发读写。
* **自动迁移**：内置数据库 Schema 会在数据库打开时自动检测并幂等迁移。
* **核心数据表**：
  - `schema_migrations`：记录数据库 Schema 版本与应用时间。
  - `backup_runs`：记录每次备份执行的快照 ID、文件变更量、新增大小、耗时与状态。
  - `restore_runs`：记录每次跨机还原的源快照、目标服务器、重映射路径、恢复文件数、耗时与状态。

为什么选单连接串行写入而不是连接池，见[架构与信任模型](../explanation/architecture.md)。

---

## 8. 相关文档 (See Also)

- [服务器与资产](server_ops.md)：服务器清单与标签检索、业务资产类型与 `ops asset` 命令、远端执行。
- [备份、还原与调度指南](backup.md)：`backups.yaml` 字段规范、备份与还原命令、Cron 调度与 `ops daemon`。
- [1Password 备份与跨机同步指南](onepassword.md)：凭据的加密备份、跨机同步与恢复。
- [命令行参考](cli.md)：全部命令与 flag 的机械清单（由 `make docs-gen` 生成）。
