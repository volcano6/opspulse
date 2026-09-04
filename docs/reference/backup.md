# 备份管理指南 (Backup Management)

OpsPulse 通过声明式 YAML 配置统一编排多主机的数据备份任务，底层调度工业级备份工具 `restic`，并将每次备份的快照 ID、文件变更量、新增数据大小、耗时与状态持久化记录到本地 SQLite 数据库中。

---

## 1. 配置文件 `backups.yaml`

路径：`$XDG_CONFIG_HOME/opspulse/backups.yaml`

### 配置示例

```yaml
backups:
  - name: web-data
    server: vps-01                          # 关联 servers.yaml 中的服务器名称，或 "local"
    paths:
      - /var/www
      - /etc/nginx
    backend: s3:s3.amazonaws.com/my-backup-bucket # 或本地路径 /mnt/backup/repo
    schedule: "0 2 * * *"                   # 定时调度 cron 表达式（可选）
    env:
      AWS_ACCESS_KEY_ID: "your-access-key"
      AWS_SECRET_ACCESS_KEY: "your-secret-key"
      RESTIC_PASSWORD: "your-restic-password"
    retention:                              # 自动修剪与快照保留策略
      keep_daily: 7                         # 保留最近 7 天的每日快照
      keep_weekly: 4                        # 保留最近 4 周的每周快照
      keep_monthly: 6                       # 保留最近 6 个月的每月快照
    excludes:                               # 排除规则
      - "*.log"
      - "*.tmp"
      - ".cache"
    tags:
      - prod
      - web
    description: 生产 Web 站点与 Nginx 配置备份

  - name: local-configs
    server: local                           # 在运行 OpsPulse 的本地主机上执行
    paths:
      - ~/.config
    backend: /mnt/backups/local-repo
    schedule: "@daily"                      # 快捷宏调度
    env:
      RESTIC_PASSWORD: "my-local-password"
    retention:
      keep_last: 5
    description: 本地配置备份
```

---

## 2. 字段详细规范

| 字段 | 类型 | 是否必填 | 说明 |
|------|------|----------|------|
| `name` | 字符串 | **是** | 备份任务的唯一标识名称 |
| `server` | 字符串 | **是** | 目标主机。可填 `servers.yaml` 中的服务器名，或填 `local`（本机执行） |
| `paths` | 字符串列表 | 否 | 待备份的文件或目录绝对路径列表（与 `assets` 至少填一项） |
| `assets` | 字符串列表 | 否 | 关联的业务资产 ID 列表，自动解析其 Source 路径 |
| `backend` | 字符串 | **是** | Restic 仓库地址（支持 S3、B2、Azure、SFTP、本地目录等所有 restic 支持的后端） |
| `schedule` | 字符串 | 否 | Cron 调度表达式（如 `"0 2 * * *"` 或 `"@daily"`），详见 [调度指南](scheduler.md) |
| `env` | 键值对映射 | 否 | 运行时注入的环境变量（如 `RESTIC_PASSWORD`, `AWS_ACCESS_KEY_ID` 等） |
| `retention` | 对象 | 否 | 快照保留策略（`keep_daily`, `keep_weekly`, `keep_monthly`, `keep_yearly`, `keep_last`, `keep_tags`） |
| `excludes` | 字符串列表 | 否 | 排除模式匹配规则 |
| `tags` | 字符串列表 | 否 | 分类标签 |
| `description` | 字符串 | 否 | 任务描述信息 |

> **凭据安全**：`env` 中的值会以明文写入权限为 `0600` 的 `backups.yaml`，并在执行时导出为远端或本地进程环境变量。不要共享该文件；当前版本尚不支持系统 Keyring 或外部 Secret Provider。

---

## 3. CLI 命令与实战

### 查看配置的任务
```bash
opspulse backup list
```

### 模拟执行 (Dry Run)
在不真实连接或运行备份的情况下，预览生成的 restic 脚本与执行动作：
```bash
opspulse backup run web-data --dry-run
```

### 执行传统声明式任务备份
```bash
# 执行单个任务
opspulse backup run web-data

# 并发执行多个任务（例如限制最大并发数为 2）
opspulse backup run web-data,local-configs --parallel 2

# 执行全部配置的备份任务
opspulse backup run all
```

### 一键容器智能备份 (无需前置配置)
无需提前编写 YAML，直接对目标 VPS 上的运行容器进行智能热备份：
```bash
# 直接备份远端 vps-01 上的 my-app 容器与挂载数据
opspulse backup run vps-01:my-app

# 备份时重命名（例如将测试容器 nginx-test 转换为规范的 nginx）
opspulse backup run vps-01:nginx-test --as nginx
```
> **自动处理**：
> - 自动探测是否为 Compose 项目，非 Compose 则自动逆向反编译生成 `compose.yaml`。
> - 若为 MySQL / PostgreSQL 数据库，自动执行容器内在线热 Dump 并管道压缩。
> - 自动继承默认存储仓库并将配置持久化登记至 `backups.yaml` 与 `assets.yaml`。
> - 详见 [容器迁移指南](../tutorial/container_migration.md)。

### 查看最新备份状态
展示所有任务的最新一次备份运行时间、状态、快照 ID、新增数据量及总容量：
```bash
opspulse backup status
```

### 查看历史运行记录
从 SQLite 数据库中调取指定任务的历次执行历史：
```bash
opspulse backup history web-data --limit 10
```

### 查询远端仓库快照
直接连接目标仓库，列出实际存储的所有快照列表：
```bash
opspulse backup snapshots web-data
```
