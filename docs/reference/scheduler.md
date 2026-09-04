# 定时调度与自动化指南 (Scheduler & Automation)

OpsPulse 内置基于标准 Cron 规范的调度引擎，支持通过守护进程（Daemon）在后台自动按计划执行备份任务，并在任务执行完毕或出现故障时自动触发告警通知。

---

## 1. 核心特性

- **标准 Cron 语法支持**：支持标准的 5 位 Cron 表达式（分、时、日、月、周）与常用快捷宏（如 `@daily`、`@hourly`、`@weekly`、`@every 2h`）。
- **防重叠并发保护 (Skip-If-Running)**：当上一次备份任务耗时较长、下一次调度触发时刻已到时，调度器会自动跳过本次执行，防止多实例重复争抢远端带宽或磁盘 I/O。
- **告警自动联动**：任务执行结束后，调度器会自动收集备份状态、快照 ID、文件变更量与耗时，通过 `internal/notify` 自动分发到配置的 Webhook 渠道（如仅在失败时告警）。
- **优雅退出 (Graceful Shutdown)**：响应系统 `SIGINT` / `SIGTERM` 信号，等待正在执行中的备份任务完成并安全落库后退出（超时 30 秒兜底保护）。
- **单次批量执行模式 (`--once`)**：支持一次性按序执行全部已配置调度的备份任务并立即退出，完美适配 Linux 宿主系统的外部 `crontab` 或 `systemd timer`。

---

## 2. 任务调度配置 (`backups.yaml`)

在 `$XDG_CONFIG_HOME/opspulse/backups.yaml` 中为指定备份任务增加 `schedule` 字段：

```yaml
backups:
  # 每天凌晨 2:00 自动备份网站数据
  - name: web-data
    server: prod-vps
    paths:
      - /var/www
      - /etc/nginx
    backend: s3:s3.amazonaws.com/my-backup-bucket
    schedule: "0 2 * * *"
    env:
      AWS_ACCESS_KEY_ID: "your-key"
      AWS_SECRET_ACCESS_KEY: "your-secret"
      RESTIC_PASSWORD: "your-password"
    retention:
      keep_daily: 7
      keep_weekly: 4

  # 每 6 小时自动备份数据库
  - name: db-backup
    server: prod-vps
    paths:
      - /var/lib/mysql
    backend: s3:s3.amazonaws.com/my-backup-bucket
    schedule: "0 */6 * * *"
    env:
      RESTIC_PASSWORD: "your-password"

  # 使用快捷宏：每天午夜备份
  - name: local-configs
    server: local
    paths:
      - ~/.config
    backend: /mnt/backups/local-repo
    schedule: "@daily"
    env:
      RESTIC_PASSWORD: "local-password"
```

### 常用 Schedule 表达式示例

| 表达式 | 说明 |
|:---|:---|
| `0 2 * * *` | 每天凌晨 02:00 执行 |
| `30 3 * * 0` | 每周日凌晨 03:30 执行 |
| `0 */4 * * *` | 每 4 小时整点执行一次 |
| `*/30 * * * *` | 每 30 分钟执行一次 |
| `@hourly` | 每小时开始时执行（等同于 `0 * * * *`） |
| `@daily` | 每天午夜 00:00 执行（等同于 `0 0 * * *`） |
| `@weekly` | 每周日午夜执行（等同于 `0 0 * * 0`） |
| `@every 1h30m` | 每隔 1 小时 30 分钟周期执行一次 |

> [!NOTE]
> 如果某个备份任务未配置 `schedule` 字段或留空，该任务将仅支持手动通过 `opspulse backup run <name>` 触发，不会被调度器自动执行。

---

## 3. CLI 运行方式

### 前台运行守护进程

```bash
opspulse daemon
```

输出示例：
```text
[scheduler] 📋 Registered 2 scheduled backup job(s):
  - web-data         [0 2 * * *] next: 2026-09-04 02:00:00
  - db-backup        [0 */6 * * *] next: 2026-09-03 20:00:00
[scheduler] 🚀 Daemon started. Waiting for schedule triggers (press Ctrl+C to stop)...
```

### 单次执行所有调度任务 (`--once`)

适合由系统自带的 cron 调度，或者在维护时手动执行一次全部定时任务：

```bash
opspulse daemon --once
```

---

## 4. 生产环境部署 (Systemd)

在生产 VPS 或管理机上，推荐将 OpsPulse 注册为 systemd 服务长期保持后台运行：

创建 `/etc/systemd/system/opspulse.service`：

```ini
[Unit]
Description=OpsPulse Automated Backup Scheduler Daemon
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
# 指定执行用户与环境路径（如需自定义配置目录可注入 OPSPULSE_HOME）
Environment="PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin"
ExecStart=/usr/local/bin/opspulse daemon
Restart=always
RestartSec=10s
KillMode=mixed
TimeoutStopSec=35s

[Install]
WantedBy=multi-user.target
```

启动并设置开机自启：

```bash
systemctl daemon-reload
systemctl enable --now opspulse
systemctl status opspulse
```
