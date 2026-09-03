# 业务资产模型指南 (Asset Model)

在 OpsPulse 的架构中，**Asset（资产）** 是承载服务器有状态业务数据的核心概念，用于将无序的磁盘目录升维为结构化的业务单元。

---

## 1. 核心设计原则

1. **Asset 具有全局稳定的唯一 ID (Stable ID)**：
   - 跨机器迁移或跨版本迭代时，Asset 的 `ID` 保持不变（如 `blog-mysql`）。
   - 原始路径（`source`）仅代表当前机器上的位置，在跨机迁移或还原时可以被灵活重映射（Remap）。
2. **区分无状态环境与有状态资产**：
   - **Template**：无状态的基础系统安装（如 Docker、UFW 防火墙、Fail2ban）。
   - **Asset**：有状态的业务数据（Docker Compose 项目、Volume 挂载卷、数据库逻辑 Dump、Nginx 站点配置、SSL 证书）。
3. **支持部分还原与灵活重映射**：
   - 用户可以针对单一 Asset 进行独立备份与精准还原，而不必每次全盘机械复制。

---

## 2. 内置资产类型 (Asset Types)

| 类型标识 | 适用场景 | 关键字段 |
|:---|:---|:---|
| `docker_compose` | 完整的 Docker Compose 项目目录 | `source` 指向包含 `compose.yaml` 的目录 |
| `volume` | Docker 命名数据卷或挂载数据目录 | `source` 指向宿主机挂载目录或数据卷路径 |
| `database` | 数据库逻辑导出 Dump | `source`, `engine` (mysql/postgres), `container` |
| `directory` | 通用配置或静态文件目录（如 Nginx 站点） | `source`, `excludes` |
| `file` | 单个关键文件或证书文件组（如 SSL 证书） | `source` |

---

## 3. 配置文件 `assets.yaml` 示例

路径：`$XDG_CONFIG_HOME/opspulse/assets.yaml`

```yaml
assets:
  # Docker Compose 项目
  - id: blog-compose
    type: docker_compose
    source: /opt/blog
    description: "Ghost 博客 Compose 项目"

  # MySQL 数据库资产
  - id: blog-mysql
    type: database
    source: /var/lib/mysql
    engine: mysql
    container: blog-db
    description: "博客数据库数据"

  # Nginx 虚拟主机配置目录
  - id: blog-nginx
    type: directory
    source: /etc/nginx/sites-enabled
    description: "Nginx 反向代理配置"

  # SSL 证书文件
  - id: blog-ssl
    type: file
    source: /etc/letsencrypt
    description: "Let's Encrypt SSL 证书"
```

---

## 4. CLI 命令

### 添加资产

```bash
# Docker Compose 项目
opspulse asset add blog-compose --type docker_compose --source /opt/blog --desc "Ghost 博客"

# 数据库资产（指定引擎与容器名）
opspulse asset add blog-mysql --type database --source /var/lib/mysql --engine mysql --container blog-db --desc "博客数据库"

# 通用目录（支持排除模式）
opspulse asset add blog-nginx --type directory --source /etc/nginx/sites-enabled --excludes "*.bak,*.tmp" --desc "Nginx 配置"

# 单文件或文件组
opspulse asset add blog-ssl --type file --source /etc/letsencrypt --desc "SSL 证书"
```

### 查看资产

```bash
# 列出所有资产
opspulse asset list

# 查看单个资产详情
opspulse asset show blog-mysql
```

### 删除资产

```bash
opspulse asset remove blog-mysql
```

### `asset add` 完整参数

| 参数 | 是否必填 | 说明 |
|:---|:---|:---|
| `<id>` | **是** | 资产唯一标识（仅字母、数字、`-`、`_`） |
| `--type` | **是** | 资产类型：`docker_compose`、`volume`、`database`、`directory`、`file` |
| `--source` | **是** | 服务器上的源路径 |
| `--engine` | 否 | 数据库引擎（`mysql`、`postgres`），仅 `database` 类型使用 |
| `--container` | 否 | Docker 容器名称，仅 `database` 类型使用 |
| `--excludes` | 否 | 逗号分隔的排除 Glob 模式 |
| `--desc, -d` | 否 | 资产描述 |

---

## 5. 与 Backup / Restore 的协同

1. **Restore 精准还原**：在还原时，通过 `--asset blog-mysql` 精准还原单个资产，仅恢复该资产 `source` 路径下的文件。
2. **跨机迁移重映射**：配合 `--target-server new-vps --target-path /data/blog` 实现路径重映射与跨机迁移。
3. **Dry-Run 预览**：通过 `opspulse restore run <job> --asset blog-mysql --dry-run` 预览将被还原的文件列表，不执行实际写入。

