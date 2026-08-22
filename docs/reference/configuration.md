# 配置与存储规范

OpsPulse 严格按照各操作系统的 XDG Base Directory 规范组织配置文件与运行时数据目录。

---

## 1. 目录结构

| 目录类型 | Linux 默认路径 | macOS 默认路径 | Windows 默认路径 | 环境变量覆盖 |
|---------|---------------|---------------|-----------------|-------------|
| **配置目录** | `~/.config/opspulse/` | `~/Library/Application Support/opspulse/` | `%APPDATA%\opspulse\` | `$OPSPULSE_HOME` / `$XDG_CONFIG_HOME` |
| **数据目录** | `~/.local/share/opspulse/` | `~/Library/Application Support/opspulse/data/` | `%LOCALAPPDATA%\opspulse\` | `$OPSPULSE_HOME/data` / `$XDG_DATA_HOME` |

---

## 2. 服务器清单 (`servers.yaml`)

路径：`$XDG_CONFIG_HOME/opspulse/servers.yaml`

### `servers.yaml` 配置示例

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
    description: 生产环境主 Web 节点

  - name: db-01
    host: 198.51.100.20
    port: 2222
    user: admin
    tags:
      - prod
      - database
    description: 主 PostgreSQL 数据库节点
```

### 字段说明

| 字段 | 类型 | 默认值 | 详细说明 |
|------|------|--------|---------|
| `name` | 字符串 | **必填** | 服务器唯一标识名 |
| `host` | 字符串 | **必填** | IP 地址或域名 |
| `port` | 整数 | `22` | SSH 端口号 |
| `user` | 字符串 | `root` | SSH 登录用户名 |
| `key_path` | 字符串 | `""` | 私钥文件路径（支持 `~` 自动展开）。若为空，OpsPulse 会自动扫描 `~/.ssh/id_ed25519`、`id_rsa`、`id_ecdsa` |
| `password` | 字符串 | `""` | SSH 密码（当未指定密钥或密钥不可用时的备用方式） |
| `tags` | 字符串列表 | `[]` | 分组标签（便于后续按标签批量执行） |
| `description` | 字符串 | `""` | 备注描述信息 |

---

## 3. SQLite 数据库 (`opspulse.db`)

路径：`$XDG_DATA_HOME/opspulse/opspulse.db`

OpsPulse 使用纯 Go 实现的 `modernc.org/sqlite` 驱动管理状态与历史指标：

* **WAL 模式**：默认开启 Write-Ahead Logging，支持高并发读写，零锁冲突。
* **自动迁移**：内置的数据库 Schema 脚本（`migrations/*.sql`）会在每次应用启动时在事务内自动检测并幂等迁移。
