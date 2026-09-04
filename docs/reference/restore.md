# 还原管理指南 (Restore Management)

OpsPulse 提供基于 restic 快照的精准还原能力，支持同机还原、跨机迁移、单资产精准恢复与 Dry-Run 预览。每次还原操作的状态、耗时与目标信息均持久化记录到本地 SQLite 数据库中。

---

## 1. 核心概念

| 概念 | 说明 |
|:---|:---|
| **同机还原** | 将快照还原到备份的原始服务器和原始路径 |
| **跨机迁移并直接启动** | 还原到新服务器后**默认自动拉起容器并自动灌库** |
| **容器改名** | 通过 `--as` 在还原启动时指定新服务/容器名 |
| **仅恢复文件** | 通过 `--no-start` 跳过容器启动和数据库灌入 |
| **路径重映射** | 通过 `--target-path` 将数据还原到不同的目录结构 |
| **单资产精准还原** | 通过 `--asset` 仅还原指定业务资产的文件 |
| **Dry-Run 预览** | 通过 `--dry-run` 列出将被还原的文件列表，不写入任何数据 |

---

## 2. CLI 命令

### 执行还原

```bash
# 还原最新快照到原始服务器和路径
opspulse restore run web-data

# 跨机迁移：还原到新 VPS 并默认自动拉起容器（无需额外参数！）
opspulse restore run my-app --target-server new-vps

# 跨机迁移时改名
opspulse restore run my-app --target-server new-vps --as clean-app

# 仅恢复文件，不自动启动容器
opspulse restore run my-app --target-server new-vps --no-start

# 指定特定快照 ID
opspulse restore run web-data --snapshot abc12345

# 跨机迁移 + 路径重映射
opspulse restore run web-data --target-server new-vps --target-path /data/web

# 单资产精准还原（仅恢复 blog-mysql 资产路径下的文件）
opspulse restore run web-data --asset blog-mysql

# Dry-Run：预览文件列表
opspulse restore run web-data --dry-run
```

### 查看还原历史

```bash
# 查看所有还原历史
opspulse restore history

# 按任务名筛选
opspulse restore history web-data

# 限制显示条数
opspulse restore history web-data --limit 5
```

---

## 3. `restore run` 完整参数

| 参数 | 默认值 | 说明 |
|:---|:---|:---|
| `<job-name>` | — | 备份任务名称（必填，对应 `backups.yaml` 中的 `name`） |
| `--snapshot` | `latest` | 快照 ID，或 `latest` 自动查询最新快照 |
| `--target-server` | 与源相同 | 目标服务器名（用于跨机迁移） |
| `--target-path` | `/`（原路径） | 还原目标路径（用于路径重映射） |
| `--as` | （原名） | 重命名还原后的容器/服务名 |
| `--no-start` | `false` | 抑制自动启动：仅解压文件，不拉起容器也不灌库 |
| `--asset` | （空=全部还原） | 指定资产 ID，仅还原该资产对应的文件 |
| `--dry-run` | `false` | 预览模式：仅列出文件，不执行实际还原 |

---

## 4. 工作流示例

### 场景 1：日常同机全量还原

```bash
# 查看可用快照
opspulse backup snapshots web-data

# 还原最新快照到原始位置
opspulse restore run web-data
```

### 场景 2：VPS 到期迁移

```bash
# 1. 在旧机器上备份
opspulse backup run web-data

# 2. 在新机器上还原（跨机 + 路径重映射）
opspulse restore run web-data --target-server new-vps --target-path /opt/web

# 3. 查看还原历史确认结果
opspulse restore history web-data
```

### 场景 3：精准还原单个数据库

```bash
# 仅还原 blog-mysql 资产的文件（不影响其他数据）
opspulse restore run web-data --asset blog-mysql

# 先预览将还原哪些文件
opspulse restore run web-data --asset blog-mysql --dry-run
```

---

## 5. 数据持久化

每次还原操作（包括 Dry-Run）均自动记录到 SQLite 数据库中，通过 `opspulse restore history` 可查看：

| 字段 | 说明 |
|:---|:---|
| `ID` | 执行记录唯一 ID |
| `JOB` | 关联的备份任务名称 |
| `STATUS` | 执行状态（`SUCCESS`、`FAILED`、`DRY-RUN`） |
| `SNAPSHOT` | 还原使用的快照 ID（截断为前 8 位） |
| `SOURCE` | 源服务器名称 |
| `TARGET` | 目标服务器名称（跨机时显示不同值） |
| `DURATION` | 执行耗时 |
| `STARTED AT` | 开始时间 |
