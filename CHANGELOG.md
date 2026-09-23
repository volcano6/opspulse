# Changelog

本项目的所有重要变更都记录在此。
格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)。

## [0.4.0]

### 新增

- 中性化门禁 `scripts/check-neutrality.sh`：按**形状**扫描全部已跟踪文件，拦截非文档段 IP、真实邮箱域、
  真实家目录、私钥正文与非默认 `op://` 库名。CI 新增独立 `neutrality` job，`make ci` 同步接入；
  规则边界与写法约定见 `CONTRIBUTING.md` 的「中性化约定」。
- README 新增「📦 安装」章节：预编译包下载、校验与解压即用（此前只有源码编译一条路径）。
- 本文件 `CHANGELOG.md`。

### 变更

- Release notes 改为手写：`release.yaml` 不再使用 `generate_release_notes`，变更记录以本文件为准。
- `.gitignore` 补齐常见 AI 工具目录、构建产物、运行日志、本地密钥与补丁残留文件。
- **破坏性：批量并发标志由 `-p` 改为 `-j`**（`ops exec`、`ops doctor`、`ops backup run` 等）。`-p` 现在只表示
  `--port`，不再与端口混淆；长名 `--parallel` 不变。
- **破坏性：`ops logs` 只保留长标志** `--follow` / `--timestamps`（移除 `-f` / `-t` 短名）；`--tail` / `-n` 不变。
- **破坏性：`ops add --tags` 只保留长标志**（移除 `-t` 短名）。`-t` 在 `ops bootstrap` 中表示模板，同名短标志在两处含义不同容易误用。
- **破坏性：`ops exec` 不再解析命令之后的标志**：`ops exec web df -h` 现在按字面把 `-h` 交给远端，
  服务器名与 `--filter` 混用、或把 `--filter` 写在命令之后，都会直接报错而不是静默按某种顺序解释；
  确需传递以 `-` 开头的远端参数时用 `--` 分隔。
- **破坏性：`ops export ssh-config --file` 必须与 `--write` 同时使用**。此前 `--file` 被静默忽略，文件根本不会生成。
- **破坏性：`ops server remove` 增加确认提示**，非交互场景需显式 `--yes`。
- **破坏性：`ops backup run <server>:<container> --dry-run` 现在直接报错**。这种直接备份容器的形式没有预览模式，
  旧版会忽略该标志并真的执行备份；需要预览请用声明式任务 `ops backup run <job> --dry-run`。
- `ops server add/set --port 0` 明确表示「未设置」，保存时回落为默认的 22（旧版拒绝 0）。
- **破坏性：`ops add` / `ops server add` 会把服务器名里的下划线统一改写为中划线**（`ops add web_1` 保存为
  `web-1`），改写后与已有条目重名时直接报错而不是覆盖。`web_1` 与 `web-1` 在多数字体里几乎一样，此前会让同一台
  机器在清单里出现两次。已存在的条目不会被改名；清单里已有下划线名字时，再次添加其中划线形式会提示二者只差下划线。
- 文档与实现对齐：`ops doctor` 的说明改为「跨全部已配置服务器的只读巡检」（SSH 连通性、根分区占用、Docker 状态），
  不再是「本地环境体检」；`CONTRIBUTING.md` 的 Go 版本要求与 `go.mod` 对齐。

### 移除

- 删除从未被使用的 `.goreleaser.yaml`（发行流程实际由 `.github/workflows/release.yaml` 承担）。
- 删除已退役的隐藏命令 `ops 1p push` / `ops 1p pull`，以及已废弃的隐藏标志 `ops sftp --cleanup`。
- 备份脚本不再执行 `restic self-update`：目标主机的 restic 低于 0.16.0 时改为**探测后省略 `--retry-lock`**，
  OpsPulse 不再静默升级目标主机上的二进制。
- 清理死代码与构建卫生：`scripts/verify.sh`、`scripts/install-hooks.sh`、`docker-compose.dev.yml`、
  `translateWSLPathToWindows`、`ListMaterialized1PKeys` 与 `Makefile` 的 `dev` 目标。

### 修复

- **远端可见的凭据**：脚本小于 48KB 时会被 base64 内联进远端命令行，脚本 env 块中的 `RESTIC_PASSWORD`
  等明文对目标主机上任何能执行 `ps` 的用户可见。现无条件经 stdin 传输。
- **非法端口被静默改写**：`servers.yaml` 中 `< 0` 或 `> 65535` 的端口此前被静默改成 22，现在直接报错。
- **SFTP 上传可能删掉目标文件**：`UploadFile` 在重命名前先删除目标文件，两次 rename 都失败时目标文件凭空消失；
  现只在回退分支内删除。
- **容器备份别名未校验**：`--as` 传入 `../`、`/` 或前导 `.` 会拼进 `/var/lib/opspulse/containers/<alias>`
  与 Compose 工程名，现在直接拒绝。
- **失败告警漏报**：`partial` 状态（主体成功、后续步骤失败，例如还原成功但容器未拉起）此前不触发失败告警，
  现按失败处理。
- **`ops 1p restore <name>` 白弹授权框**：此前不检查名字是否存在就先向 1Password 取授权，现在先查本地清单，
  未收录的名字立即失败并提示先执行无参数的 `ops 1p restore`。
- **`ops export ssh-config --write` 空清单谎报成功**：清单为空时此前仍打印写入成功，现在明确提示无事可做，
  且不会创建或改写 `~/.ssh/config`。
- **`ops notify test` 顺序反了**：未配置任何渠道时，此前先打印「Testing all configured notification channels...」
  再失败，读起来像投递故障；现在直接报「no notification channels configured」。
- **`ops server add` 的 `--identity` 与 `--key` 同时给出**：二者本是同一设置的两个名字，此前 `--identity` 优先、
  `--key` 被静默忽略；现在两者给出不同值时直接报错。

### 安全

- 已跟踪文件完成一次中性化：真实主机名、机队规模与本机指纹不再出现在源码、夹具与文档中。
- WSL 下为 Windows GUI 客户端镜像的私钥目录（`%USERPROFILE%\.ssh\opspulse\`）已在文档写明其位置、持久化的原因
  与手动清理方法；`ops sftp` 在密码认证下会把口令放进 GUI 客户端的命令行参数，文档已如实说明并建议改用密钥。
- 公开前对仓库历史与发行资产做了一次中性化处理。`v0.2.0` / `v0.3.0` 的发行包已于 2026-09-23 重建，
  内嵌 commit 指向重写后的提交，校验和与最初上传的文件不同，请以 release 页上的 `checksums.txt` 为准。

## [0.3.0] - 2026-08-26

### 新增

- **1Password 集成**：`ops 1p` 顶层命名空间；整台机器（本机全部私钥 + 整份 `servers.yaml`）备份进单个
  `opspulse_inventory_<hostname>` Secure Note；新机一条命令还原清单与全部凭据；`pull --all` 批量撤离
  1Password 凭据（并集合并多机备份文档，绝不删除本机独有的服务器）。
- **WSL 跨端**：跨端路径重映射、Windows GUI 唤起与私钥安全桥接、Windows `op.exe` 路径穷尽探测。
- **SSH**：`ops ssh` 交互式直连与连接复用；跳板机穿透与老旧 `ssh-rsa` 兼容；`ops export ssh-config`；
  新增 `--exec` 入口，把远程命令与 ssh 选项分开。
- **容器备份与跨机快起**：`ops backup run <server>:<container>` 直接备份野生容器并自动逆向转译为标准
  `compose.yaml`；MySQL / PostgreSQL 容器内在途热 Dump 与 gzip 即时压缩；`ops restore run` 跨机自适应
  拉起容器并自动灌库。
- **资产与还原**：`ops asset`（add / list / show / remove）与 `ops restore run` / `ops restore history`。
- **调度与通知**：基于 cron 表达式的备份守护进程（`ops daemon`，支持 `--once`）、Webhook 多渠道条件告警。
- 模板库扩充，新增国内 VPS 初始化加速模板；`ops server info` 单次 SSH 聚合采集系统与硬件信息。

### 变更

- CLI 门面统一为 `ops`，`ops cp` 合并 SFTP 双向传输；`make install` 安装路径与补全脚本 PATH 形成闭环。
- 严格主机密钥校验（Strict Host Key Checking）默认开启；WarnWriter 线程安全日志链路全量接线。
- 凭据改为本地优先：`ops ssh` / `ops exec` / `ops cp` 全程不与 1Password 交互，不再弹授权框。

### 修复

- 修复多路径备份还原误匹配兄弟服务清单、重映射路径丢失与容器还原的数据一致性问题。
- 修复并发调度的信号断链与批量执行摘要失真；空机器 restore 不再谎报保留了本机独有的服务器。
- 修复 WSL 下未知主机 askpass 死循环；`ops ssh --` 之后的裸参数立即报错。

### 安全

- 默认强制严格主机密钥校验，未知主机输出密钥类型与 SHA256 指纹后阻断连接。
- 修复文件系统与路径安全风险；移除已废弃且不安全的 `KeyAlgoDSA`。
- 私钥改由 1Password Login 条目承载，写入后回读逐字节校验。

## [0.2.0] - 2026-08-24

首个发布版本。

### 新增

- 服务器清单（`servers.yaml`）与标签管理、`ops server` 运维子命令。
- 脚本模板系统（YAML Frontmatter 元数据、同名优先覆盖）与官方模板 `base` / `docker` / `security` / `restic`。
- 纯 Go 嵌入式 SQLite 存储层与自动迁移，记录结构化执行历史与状态。
- restic 备份执行引擎、输出解析、并发备份调度池与完整 CLI（含 Dry-Run 与保留策略自动修剪）。
- Shell 动态自动补全与文档指南；GitHub Actions 构建与发布工作流。

[0.4.0]: https://github.com/volcano6/opspulse/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/volcano6/opspulse/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/volcano6/opspulse/releases/tag/v0.2.0
