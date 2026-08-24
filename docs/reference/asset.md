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

## 4. 与 Backup / Restore / Migration 的协同

1. **Backup Job 引用**：`backups.yaml` 中通过 `assets: [blog-compose, blog-mysql]` 声明需要打包的资产列表。
2. **Restore 精准还原**：在还原时，可以通过 `--asset blog-mysql` 精准还原单个数据库，或通过 `--dest /mnt/new-path` 实现路径重映射。
3. **Blueprint 蓝图画像**：Blueprint 通过引用 Asset ID，描述一台服务器需要挂载哪些业务服务。
