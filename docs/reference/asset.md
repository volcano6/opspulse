# 业务资产模型指南 (Asset Model)

Asset（资产）是仓库中的数据模型，用于描述服务器上的有状态业务数据。当前版本仅实现模型、校验与 YAML 存储层，尚未提供 `asset`、`restore`、路径重映射或 Blueprint CLI；生产操作请使用现有 `backup` 命令中的 `paths`。

---

## 1. 核心设计原则

1. **稳定 ID**：`id` 用于唯一标识资产记录。
2. **类型与来源**：`type` 描述资产类别，`source` 描述其来源路径。
3. **当前边界**：模型尚未接入备份与还原执行流程，配置 `assets.yaml` 不会改变 `backup run` 的行为。

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

## 4. 当前集成状态

- `backups.yaml` 当前仅接受 `paths`，不接受 `assets` 字段。
- 当前没有 `asset` 或 `restore` CLI 命令。
- `restore_runs` 数据表仅为存储层预留，不代表还原流程已经实现。
