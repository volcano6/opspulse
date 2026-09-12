# OpsPulse 技术债与后续优化事项追踪 (Technical Debt & Backlog)

本文档记录了在代码与逻辑审查中发现的非紧急（P2/P3/P4）但确实存在的设计局限、稳定性隐患以及日常运维功能改进点，供后续版本迭代参考。

---

## 目录

- [一、已完成的紧急与安全修复 (P0 / P1 / P2)](#一已完成的紧急与安全修复-p0--p1--p2)
- [二、稳定性与数据一致性 (P3 - 可规划至演进版本)](#二稳定性与数据一致性-p3---可规划至演进版本)
- [三、平台兼容与低收益优化 (P4 - 按需处理)](#三平台兼容与低收益优化-p4---按需处理)
- [四、日常运维功能缺口与使用体验优化](#四日常运维功能缺口与使用体验优化)

---

## 一、已完成的紧急与安全修复 (P0 / P1 / P2)

### 1. v0.8.x 批次 1 安全与注入加固闭环
1. **[P0] 彻底移除 SSH 配置与连接命令主动弱算法降级** (`internal/server/ssh_config.go`, `cmd/opspulse/ssh.go`)：
   - 清除了 `RenderSSHConfig` 以及 `ops ssh` / `ProxyCommand` 中硬编码的 `HostKeyAlgorithms +ssh-rsa,ssh-dss` 与 `PubkeyAcceptedKeyTypes +ssh-rsa`。
   - 杜绝向用户本机 SSH 客户端与导出配置中无差别注入已被 OpenSSH 废弃淘汰的 1024 位 DSA 和易受碰撞攻击的 SHA-1 RSA 算法，恢复现代默认安全协商（Ed25519, ECDSA, RSA-SHA2-256/512）。
2. **[P0] 默认强制严格主机密钥校验与显式受信机制 (Strict by Default)** (`internal/executor/auth.go`, `README.md`)：
   - 贯彻零信任原则，默认强制执行严格的主机密钥校验（Strict Host Key Checking）。对未记录在 `known_hosts` 中的未知主机直接阻断连接，输出公钥算法类型与 SHA256 指纹，并明确提示用户使用 `ssh <host>` 先行验证或设置 `OPSPULSE_TRUST_NEW_HOST_KEY=1`；
   - 自动受信（TOFU / `accept-new`）必须由用户显式设置 `OPSPULSE_TRUST_NEW_HOST_KEY=1` 开启；
   - 无论是默认严格模式还是受信模式，任何已记录主机的公钥不匹配（Mismatch）均被严格阻断，彻底消除静默信任与中间人（MITM）攻击隐患；
   - 支持通过 `OPSPULSE_KNOWN_HOSTS` 环境变量自定义已知主机文件路径，便于测试与定制化环境隔离。
3. **[P1] 主机受信告警线程安全路由与日志落盘** (`internal/executor/auth.go`, `internal/executor/ssh.go`)：
   - 彻底废除底层对 `os.Stderr` 的裸写，将 TOFU 首次受信告警通过 `warnWriter` / `safeWriter` 管道输出；
   - 保证多主机并发场景（如并发 bootstrap / batch exec）下日志原子不交织，并能被打上前缀（`[server:job]`）完整持久化至 `bootstrap-*.log` / `backup-*.log` 日志文件。
4. **[P1] remoteShellCommand 与容器 YAML 写入 base64 转义闭环** (`internal/executor/ssh.go`, `internal/backup/container_runner.go`)：
   - 对 `remoteShellCommand` 中拼接的 base64 字符串应用 `shellquote.Quote()` 强单引号转义，彻底杜绝依赖字符集假设的单引号逃逸面；
   - 在 `container_runner.go` 中对生成 Compose 和 Manifest YAML 的 base64 注入命令同步增加强转义保护。
5. **[P1] 容器备份临时 SQL Dump 全生命周期安全防护与目录权限保障** (`internal/backup/container_runner.go`, `internal/docker/dump.go`)：
   - 将此前被低估为 P4 的临时文件残留隐患升级并彻底闭环；
   - 避免全局 `umask 077` 导致 `mkdir -p` 创建的父目录丢失 `+x`（不可进入）副作用，对导出的明文 SQL 转储文件显式执行 `chmod 0600`，确保仅属主可读；
   - 在容器备份启动阶段增加前置残留清理（`rm -rf projectDir/dumps projectDir/volumes`），防止历史中断任务残留的明文 dump 被二次打包入库；
   - 退出清理 `defer` 引入独立的 15 秒超时 context，并在清理异常时输出显式高优先级告警，杜绝错误吞噬。
6. **[P1] 容器转储脚本容器名称换行注入防御** (`internal/docker/dump.go`)：
   - 严格断言 `containerName` 禁止包含 `\r` 或 `\n`，从源头阻断换行逃逸命令注入面。
7. **[P2] 澄清备份环境变量写入脚本落盘事实** (`internal/backup/script.go`, `internal/backup/runner.go`)：
   - 代码级走查证实：`logFile` 仅捕获进程执行的 stdout/stderr，`script` 内容未写入本地 `logs/` 文件，且脚本声明为 `set -euo pipefail`（无 `-x` 回显），不存在磁盘明文泄漏。

### 2. 早期及历史修复项（已闭环）
7. **[P0] Heredoc 注入 RCE 修复** (`internal/backup/container_runner.go`)：由不可信容器元数据反编译生成的 Compose/Manifest YAML 文件通过 `base64 -d` 安全下发到目标机，杜绝了 `EOF` 逃逸注入风险；同时修复 `dedupPaths` 保持 Linux 规范路径。
8. **[P0] Bootstrap 参数注入修复** (`internal/bootstrap/service.go`)：将 `%q` 替换为 `shellquote.Quote()` 单引号强转义，参数注入风险已清零。
9. **[P1] MySQL 密码含特殊字符导致备份失败** (`internal/docker/dump.go`)：将命令行 `-p$PASS` 改为官方推荐的 `export MYSQL_PWD=...` 环境变量机制，避免 shell 分词与特殊字符丢失。
10. **[P1] Windows SSH Config 写入防丢失保障** (`internal/server/ssh_config.go`)：完善 Windows 下 `os.Rename` 目标已存在时的回退机制，在替换前创建带时间戳的临时备份并在失败时自动还原，防止用户 `~/.ssh/config` 损坏或丢失。
11. **[P1] Askpass Helper 损坏 JSON 防密码泄露** (`cmd/opspulse/ssh.go`)：严格区分 JSON 配置与 legacy 纯文本密码，反序列化错误时拒绝回退输出原始内容，防止密码泄露至终端/输出。
12. **[P2-01] 脚本模板路径穿越防御 (Path Traversal)** (`internal/template/loader.go:74-77`)：已于 commit `4c6b420` 引入 `filepath.Clean`、`..` 和 `filepath.IsAbs` 严格前置断言。
13. **[P2-02] 调度器长任务优雅退出取消传导 (Context Cancellation)** (`internal/scheduler/scheduler.go:148`)：已于 commit `4c6b420` 引入 `s.daemonCtx`，在 `s.Stop()` 时传导取消信号。
14. **[P2-03] 并发输出交错保护 (PrefixedWriter Concurrency)** (`internal/executor/logger.go:56-68`)：已于 commit `4c6b420` 将 `prefix + line` 组合为单次原子写入底层 Writer。

---

## 二、稳定性与数据一致性 (P3 - 可规划至演进版本)

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

## 三、平台兼容与低收益优化 (P4 - 按需处理)

1. **Windows 平台 SSH 密钥 ACL 权限**：Windows 的 `0600` 文件权限不等于 Unix 的文件属性，可借助 Windows ACL API 移除除当前用户外的其他组读取权限。
2. **编辑器路径含空格问题**：`cmd/opspulse/server_config.go` 中的 `strings.Fields(editor)` 在路径含空格（如 `C:\Program Files\...`）时可能误切分，建议改用参数感知解析器。
3. **系统 uptime 解析兼容性**：增强针对各种非标准 Linux 发行版 uptime 格式的正则匹配容错。

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
