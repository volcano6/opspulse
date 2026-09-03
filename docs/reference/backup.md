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
| `paths` | 字符串列表 | **是** | 待备份的文件或目录绝对路径列表 |
| `backend` | 字符串 | **是** | Restic 仓库地址（支持 S3、B2、Azure、SFTP、本地目录等所有 restic 支持的后端） |
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

### 执行备份
```bash
# 执行单个任务
opspulse backup run web-data

# 并发执行多个任务（例如限制最大并发数为 2）
opspulse backup run web-data,local-configs --parallel 2

# 执行全部配置的备份任务
opspulse backup run all
```

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
