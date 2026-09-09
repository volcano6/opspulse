# OpsPulse 技术债与后续优化事项追踪 (Technical Debt & Backlog)

本文档记录了在代码与逻辑审查中发现的非紧急（P2/P3/P4）但确实存在的设计局限、稳定性隐患以及日常运维功能改进点，供后续版本迭代参考。

---

## 目录

- [一、已完成的紧急修复 (P0 / P1)](#一已完成的紧急修复-p0--p1)
- [二、设计与边界问题 (P2 - 建议在后续小版本处理)](#二设计与边界问题-p2---建议在后续小版本处理)
- [三、稳定性与数据一致性 (P3 - 可规划至演进版本)](#三稳定性与数据一致性-p3---可规划至演进版本)
- [四、平台兼容与低收益优化 (P4 - 按需处理)](#四平台兼容与低收益优化-p4---按需处理)
- [五、日常运维功能缺口与使用体验优化](#五日常运维功能缺口与使用体验优化)

---

## 一、已完成的紧急修复 (P0 / P1)

在 v0.7.x 热修复中已完成闭环的问题：
1. **[P0] Heredoc 注入 RCE 修复** (`internal/backup/container_runner.go`)：由不可信容器元数据反编译生成的 Compose/Manifest YAML 文件通过 `base64 -d` 安全下发到目标机，杜绝了 `EOF` 逃逸注入风险；同时修复 `dedupPaths` 保持 Linux 规范路径。
2. **[P0] Bootstrap 参数注入修复** (`internal/bootstrap/service.go`)：将 `%q`（双引号在 bash 中仍会执行 `$()` 命令替换）替换为 `shellquote.Quote()` 单引号强转义，参数注入风险已清零。
3. **[P1] MySQL 密码含特殊字符导致备份失败** (`internal/docker/dump.go`)：将命令行 `-p$PASS` 改为官方推荐的 `export MYSQL_PWD=...` 环境变量机制，避免 shell 分词与特殊字符丢失。
4. **[P1] Windows SSH Config 写入防丢失保障** (`internal/server/ssh_config.go`)：完善 Windows 下 `os.Rename` 目标已存在时的回退机制，在替换前创建带时间戳的临时备份并在失败时自动还原，防止用户 `~/.ssh/config` 损坏或丢失。
5. **[P1] Askpass Helper 损坏 JSON 防密码泄露** (`cmd/opspulse/ssh.go`)：严格区分 JSON 配置与 legacy 纯文本密码，反序列化错误时拒绝回退输出原始内容，防止密码泄露至终端/输出。

---

## 二、设计与边界问题 (P2 - 建议在后续小版本处理)

### 1. [P2-01] 脚本模板路径穿越防御 (Path Traversal)
- **涉及文件**：`internal/template/loader.go:76`
- **问题说明**：`customPath := filepath.Join(l.customDir, name+".sh")` 中未对 `name` 进行白名单或前缀约束。恶意或误传类似 `../../etc/cron.d/xxx` 的模板名可能跳出模板目录。
- **改进建议**：
  ```go
  cleanName := filepath.Clean(name)
  if strings.Contains(cleanName, "..") || filepath.IsAbs(cleanName) {
      return nil, fmt.Errorf("invalid template name: %q", name)
  }
  ```

### 2. [P2-02] 调度器长任务优雅退出取消传导 (Context Cancellation)
- **涉及文件**：`internal/scheduler/scheduler.go:142`
- **问题说明**：`executeJob` 中使用了 `ctx := context.Background()`，导致 `ops daemon` 在收到 Ctrl+C 或 SIGTERM 信号并进入 30 秒等待时，无法将取消信号下发给底层的 SSH/restic 备份任务，任务仍会继续运行直到超时。
- **改进建议**：在 `Scheduler` 结构体维护全局或可控的 `daemonCtx`，在 `s.Stop()` 时取消该上下文，让底层 SSH 会话触发 `SIGTERM` 优雅断开。

### 3. [P2-03] 并发输出交错保护 (PrefixedWriter Concurrency)
- **涉及文件**：`internal/executor/logger.go:56-69`
- **问题说明**：`PrefixedWriter.Write` 分两次向底层 `pw.Writer` 调用 `Write(pw.Prefix)` 和 `Write(line)`。虽然使用了 `SyncWriter`，但两次调用的锁被释放后可能被其他并发 job（如 `ops backup run all --parallel 4`）插队，导致两行前缀连在一起打印。
- **改进建议**：先在本地组合 `prefix + line`，一次性写入底层 Writer：
  ```go
  combined := append(pw.Prefix, line...)
  pw.Writer.Write(combined)
  ```

---

## 三、稳定性与数据一致性 (P3 - 可规划至演进版本)

### 1. [P3-01] 跨主机同名任务快照隔离
- **涉及文件**：`internal/backup/script.go:12`
- **问题说明**：若多台服务器共享同一个 restic 存储桶，且都配置了名为 `mysql` 的备份任务，仅依靠 `job:mysql` 标签可能查出其他服务器的历史快照。
- **改进建议**：备份时除 `job:<name>` 外追加 `--tag host:<server>`，还原查找最新快照时增加 `--host <server>` 严格限定主机边界。

### 2. [P3-02] Webhook 告警通知重试策略
- **涉及文件**：`internal/notify/webhook.go`
- **问题说明**：当前发送失败（如网络抖动、HTTP 5xx、超时）时只打印 Warning，没有做指数退避重试（Backoff Retry），偶发网络异常可能导致告警丢失。
- **改进建议**：增加 2~3 次重试（如 1s, 3s, 5s 间隔），增强弱网下通知可靠性。

### 3. [P3-03] YAML 配置文件跨进程文件锁 (Cross-process File Locking)
- **涉及文件**：`internal/server/store.go`、`backup/store.go`、`asset/store.go`
- **问题说明**：目前通过 `sync.RWMutex` 仅能在单进程内提供并发安全。如果用户在两个终端同时执行 `ops add` 或 `ops backup run`，跨进程并发写文件可能存在覆盖。
- **改进建议**：在执行写文件前获取文件锁（Unix: `flock`，Windows: `LockFileEx`），保障多进程互斥。

### 4. [P3-04] SQLite WAL 模式与连接池调优
- **涉及文件**：`internal/storage/db.go:42`
- **问题说明**：开启了 `journal_mode(WAL)`，但紧接着设置了 `conn.SetMaxOpenConns(1)`。WAL 模式最大优势是读写互不阻塞，设置单一连接会强制所有读操作排队。
- **改进建议**：可将 `MaxOpenConns` 调整为 5，并保留单一写事务，提高并发查询性能。

### 5. [P3-05] SFTP 大文件传输进度反馈
- **涉及文件**：`internal/sftp/client.go`
- **问题说明**：`ops cp` 传输几百兆的大文件时终端无动态进度条，用户无法感知当前传输速度和百分比。
- **改进建议**：在 `io.Copy` 外封装一层带百分比与速度刷新的进度计算器（如通过终端单行刷新 `\r`）。

### 6. [P3-06] 还原指标落盘完善
- **涉及文件**：`internal/backup/restore_runner.go:200`
- **问题说明**：还原完成后仅更新了状态和耗时，未从 restic 输出中解析 `FilesRestored` 和 `BytesRestored`，在 SQLite 表中记录为 0。
- **改进建议**：增加针对 `restic restore` 摘要的简单正则或行解析，补齐统计指标。

---

## 四、平台兼容与低收益优化 (P4 - 按需处理)

1. **Windows 平台 SSH 密钥 ACL 权限**：Windows 的 `0600` 文件权限不等于 Unix 的文件属性，可借助 Windows ACL API 移除除当前用户外的其他组读取权限。
2. **编辑器路径含空格问题**：`cmd/opspulse/server_config.go` 中的 `strings.Fields(editor)` 在路径含空格（如 `C:\Program Files\...`）时可能误切分，建议改用参数感知解析器。
3. **远端清理临时文件日志提示**：`container_runner.go` 中的 `cleanup-temp` 在网络断开时如果执行失败目前是静默忽略，可加 debug 日志以便排查残留。
4. **系统 uptime 解析兼容性**：增强针对各种非标准 Linux 发行版 uptime 格式的正则匹配容错。

---

## 五、日常运维功能缺口与使用体验优化

为提升工具在真实运维场景中的顺手度，建议在后续规划中加入以下功能：

1. **`ops backup status` 支持时间跨度过滤**：
   - 增加 `--since 24h` / `--since 7d` 等标志，便于巡检近期的备份健康度。
2. **全局统一日志与调试级别控制**：
   - 增加全局 `--verbose` (`-v`) 与 `--quiet` (`-q`) 标志，方便脚本调用或深度故障排查。
3. **守护进程系统服务一键部署**：
   - 提供 `ops daemon install-service`，自动在 Linux 上生成并注册 `systemd` unit 文件（`/etc/systemd/system/opspulse.service`），方便一键开机常驻。
4. **一键全量配置文件体检 (`ops doctor --config` / `ops config check`)**：
   - 一次性校验 `servers.yaml`、`backups.yaml`、`assets.yaml`、`notifications.yaml` 的 YAML 格式、网络可达性、凭据有效性与引用的 asset 是否存在。
