# 配置与存储规范

OpsPulse 严格按照各操作系统的 XDG Base Directory 规范组织配置文件与运行时数据目录。

---

## 1. 目录结构

| 目录类型 | Linux 默认路径 | macOS 默认路径 | Windows 默认路径 | 环境变量覆盖 |
|---------|---------------|---------------|-----------------|-------------|
| **配置目录** | `~/.config/opspulse/` | `~/Library/Application Support/opspulse/` | `%APPDATA%\opspulse\` | `$OPSPULSE_HOME` / `$XDG_CONFIG_HOME` |
| **数据目录** | `~/.local/share/opspulse/` | `~/Library/Application Support/opspulse/data/` | `%LOCALAPPDATA%\opspulse\` | `$OPSPULSE_HOME/data` / `$XDG_DATA_HOME` |

### 核心文件清单

| 文件路径 | 用途说明 |
|---------|---------|
| `$XDG_CONFIG_HOME/opspulse/servers.yaml` | 服务器清单配置文件 |
| `$XDG_CONFIG_HOME/opspulse/assets.yaml` | 结构化业务资产定义文件 |
| `$XDG_CONFIG_HOME/opspulse/backups.yaml` | 备份任务编排配置文件 |
| `$XDG_CONFIG_HOME/opspulse/templates/*.sh` | 自定义 Shell 脚本模板目录 |
| `$XDG_DATA_HOME/opspulse/logs/` | 执行日志文件落盘目录 |
| `$XDG_DATA_HOME/opspulse/opspulse.db` | 本地 SQLite 数据库文件 |

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
| `password` | 字符串 | 否 | `""` | SSH 密码。未绑定私钥时，`opspulse ssh` 使用它自动认证；`server setup-key` 使用它安装公钥但绝不修改远端密码 |
| `tags` | 字符串列表 | 否 | `[]` | 标签分组列表（便于按标签批量执行） |
| `labels` | 键值映射 | 否 | `{}` | 结构化元数据标签（如 `provider: oracle`, `region: sg`），支持 `server list --filter` 筛选 |
| `description` | 字符串 | 否 | `""` | 备注描述信息 |

---

## 3. 业务资产定义 (`assets.yaml`)

路径：`$XDG_CONFIG_HOME/opspulse/assets.yaml`

Asset 描述服务器上的有状态数据资产，每个资产拥有**稳定的唯一 ID**，跨机迁移或还原时通过 ID 引用并支持路径重映射（Remap）。

```yaml
assets:
  - id: blog-compose
    type: docker_compose
    source: /opt/blog
    description: Ghost 博客 Docker Compose 项目

  - id: blog-mysql
    type: database
    source: /var/lib/mysql
    engine: mysql
    container: blog-db
    description: 博客主数据库

  - id: blog-nginx
    type: directory
    source: /etc/nginx/sites-enabled
    description: Nginx 虚拟主机配置

  - id: blog-ssl
    type: file
    source: /etc/letsencrypt
    description: SSL 证书目录
```

---

## 4. SQLite 数据库 (`opspulse.db`)

路径：`$XDG_DATA_HOME/opspulse/opspulse.db`

OpsPulse 使用纯 Go 实现的 `modernc.org/sqlite` 驱动管理状态与历史指标：

* **WAL 模式**：默认开启 Write-Ahead Logging，支持高并发读写，零锁冲突。
* **自动迁移**：内置的数据库 Schema 脚本（`migrations/*.sql`）会在应用启动时自动检测并幂等迁移。
* **核心数据表**：
  - `schema_migrations`：记录数据库 Schema 版本与应用时间。
  - `backup_runs`：记录每次备份执行的快照 ID、文件变更量、新增大小、耗时与状态。
  - `restore_runs`：记录每次跨机还原的源快照、目标服务器、重映射路径、恢复文件数、耗时与状态。
