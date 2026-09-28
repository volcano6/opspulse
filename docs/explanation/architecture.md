# 架构与信任模型 (Architecture and Trust Model)

OpsPulse 是一个单二进制 CLI：它不常驻、不自建服务端，本机存一份清单，远端跑一段脚本。
这篇讲它由哪些部件组成、一条命令的真实执行路径、哪些数据会离开本机，以及它明确**不**做什么。

命令与参数见[命令行参考](../reference/cli.md)，配置与数据目录见[配置与模板](../reference/configuration.md)，
漏洞上报渠道与支持版本见 [SECURITY.md](../../SECURITY.md)。

## 1. 组件与数据流 (Components and Data Flow)

一个二进制、两种叫法：`ops` 与 `opspulse`（`cmd/opspulse/main.go`）。除可选的常驻调度进程
（`ops daemon`）外，每个命令都是一次性进程，跑完即退出。

| 层 | 位置 | 职责 |
|---|---|---|
| 命令层 | `cmd/opspulse/` | cobra 命令树（53 个命令）：参数解析、交互提示、输出编排 |
| 领域层 | `internal/` 下的 `server`、`asset`、`backup`、`docker`、`secret`、`template`、`bootstrap`、`doctor`、`scheduler`、`notify` | 清单模型、备份与还原语义、容器与数据库细节、凭据解析、模板、巡检、调度、告警 |
| 底座 | `internal/` 下的 `executor`、`sftp`、`storage`、`config`、`filelock`、`logger`、`shellquote`、`cliutil` | SSH 与本地执行、SFTP、SQLite、路径解析、文件锁、日志、shell 转义 |

一次备份的数据流：读本机配置目录的 YAML（清单、作业、资产）→ 在内存里生成一段 bash 脚本
→ 脚本正文经 SSH 的 **stdin** 交给远端 shell → 远端 `restic` / `docker` / `mysqldump` 干活
→ 退出码与输出回流 → 结果写进本机 SQLite 与日志文件。

本机只有两类持久化：配置目录下的 4 个 YAML 与 `templates/`，数据目录下的 `opspulse.db`
与 `logs/`。**没有**自建 HTTP 服务、没有监听端口、没有遥测上报端点；唯一由 OpsPulse 主动
发起的出站 HTTP 是告警 webhook。

外部程序都复用你机器上既有的工具链，OpsPulse 不自带：远端是 `restic`、`docker`、
`mysqldump` / `pg_dumpall`、`gzip`、`tar`、`base64`、`sh` / `bash`；本机是 `ssh`、`sftp`、
`ssh-keygen`、`op`、`$EDITOR`，以及可选的 GUI SFTP 客户端。

## 2. 执行模型 (Execution Model)

### 2.1 两条互不相同的 SSH 通路

| 通路 | 谁在用 | 实现 |
|---|---|---|
| 库内 SSH | `exec`、`cp`、`backup`、`restore`、`doctor`、`ps`、`logs`、`server info`、`bootstrap` | `golang.org/x/crypto/ssh`（`internal/executor/ssh.go`） |
| 系统 `ssh` / `sftp` 二进制 | 交互式 `ops ssh`、`ops sftp`、GUI 客户端 | `exec` 系统二进制（`cmd/opspulse/ssh.go`、`internal/sftp/launcher.go`） |

分成两条是因为诉求不同：非交互命令要能脚本化、要能把远端退出码原样传回来，所以走库内实现；
交互式终端要原生 PTY、要能用系统 ssh-agent 与 `~/.ssh/config`，所以让位给系统客户端。
代价是两者的跳板与主机密钥行为由各自实现，而认证与凭据裁决共用同一份代码
（`internal/executor/auth.go`）。

- **脚本一律经 stdin 下发**，绝不内联进命令行参数：内联会让脚本里携带的凭据出现在远端
  `ps(1)` 的进程列表中。
- **提权不交互**：远端不是 root 且 `sudo -n true` 成功时以 `sudo -E bash -s` 执行，否则以
  当前登录用户执行。`sudo -n` 不会等待密码输入，因此不会挂住自动化。
- **跳板只有一跳**：库内路径用 `direct-tcpip` 建隧道，交互式路径用 `ProxyCommand=ssh -W`。
- **本地目标**：作业的 `server: local` 表示在本机执行同一套脚本（`<shell> -s`），语义与远端一致。

各命令的具体用法、参数与输出见[服务器与资产](../reference/server_ops.md)。

### 2.2 并发与失败语义

- 并发统一由 `-j/--parallel` 控制，默认 **5**，`unlimited` 取消上限；`ops exec`、`ops doctor`
  用信号量，`ops backup run` 用作业池。调度器用 `skip-if-running` 防止同一个定时作业重入，
  **不用锁文件**——多台机器各跑各的调度器不会互相阻塞。
- 退出码：远端退出码原样透传（`executor.ExecutionError`）；批量执行中只要有一台失败，命令
  就以非零退出。`ops exec` 的 stdout 只承载远端输出，OpsPulse 自己的诊断信息走 stderr，
  因此可以安全地重定向 stdout。
- 状态机：一次备份从 `running` 到 `success` 或 `failed`；一次还原如果 restic 成功但容器没能
  自动拉起，状态是 `partial` 且命令返回非零——「数据回来了但服务没起来」不能算成功。
- **没有自动重试**：备份与还原失败不会重试。唯一的重试在告警 webhook（网络错误最多 3 次）
  与 1Password CLI 的 WSL 互操作路径上。
- 本机写入互斥：每个 YAML 的保存都持一把跨进程文件锁（`<文件>.lock`），并以
  「临时文件 → `0600` → rename」的方式原子替换；SQLite 用单连接 + `busy_timeout`。

## 3. 安全边界与信任模型 (Security Boundary and Trust Model)

### 3.1 信任根是本机磁盘

`servers.yaml` 是唯一事实源：连哪台机器、用什么凭据，只由本机的这份文件决定。凭据只以三种
形态存在——进程环境变量（`env` 注入）、本机磁盘明文（文件权限 `0600`）、或 1Password 保险库
（可选）。没有第四种：OpsPulse 不保存、也不托管任何远端凭据副本。

认证按固定优先级挑选（`internal/executor/auth.go`）：1Password SSH agent → 服务器指定的私钥
（库内路径只提交这一把；系统 `ssh` 路径再加 `IdentitiesOnly=yes`）→ 服务器上的明文密码 → 默认私钥
（`~/.ssh/id_ed25519`、`id_rsa`、`id_ecdsa` 中第一个可解析的）。四种都没有时直接报认证错误，
不做任何降级尝试。

### 3.2 主机密钥默认 fail-closed

未知主机**拒绝连接**并打印密钥类型与 SHA256 指纹；只有显式设置
`OPSPULSE_TRUST_NEW_HOST_KEY=1` 才会在首次连接时受信并写入 `known_hosts`（之后应及时取消该
变量）。已记录的主机密钥发生变化时同样拒绝，并提示用 `ssh-keygen -R <host>` 清除旧记录。
「连不上」优先于「连错机器」。

### 3.3 凭据在传输与进程上的暴露面

- **私钥永不外传**：推到远端的只有公钥（`ops add`、`server setup-key` 写
  `~/.ssh/authorized_keys`）；私钥文件始终留在本机。
- **交互式 ssh 的密码**不进入命令行参数、环境变量或进程列表：它经一个 `0600` 的受限临时文件
  交给 OpenSSH 的 `SSH_ASKPASS` 助手，连接退出后临时目录被写零并删除。
- **远端进程环境是暴露面**：作业的 `env`（含 restic 仓库密码等）会随脚本进入远端进程环境，
  远端上能读该进程的 root 可以看见它。这是刻意的取舍——备份必须在数据所在的机器上执行；
  敏感数据不出现在命令行参数里只是把它从 `ps(1)` 里拿掉，不是把它从远端藏起来。
- **脚本以登录用户或 sudo root 运行**：OpsPulse 不做远端最小权限隔离，也不审计远端。

### 3.4 什么会离开本机

| 方向 | 内容 | 说明 |
|---|---|---|
| 到远端 | 清单里的连接信息、脚本正文、该作业的 `env`、被备份/还原的路径 | 经加密的 SSH 信道 |
| 到 1Password | 仅通过 `op` 子进程，OpsPulse 不直连 1Password 服务 | 见 §3.5 |
| 到 webhook | 作业名、状态、耗时等事件摘要，以及你在 URL 里带的 token | 不含凭据正文 |
| 到 restic 后端 | 由远端（或本地目标）上的 restic 直接连接 | OpsPulse 不代理、不转发 |

### 3.5 凭据生命周期：本机明文 ↔ 1Password

- **备份**：`ops 1p backup` 把整份 `servers.yaml` 与本机全部私钥装进**一个**以本机名命名的
  Secure Note（`opspulse_inventory_<主机名>`），稳定态只花 3 次 `op` 调用，而不是每个凭据一次。
  它**不改写** `servers.yaml`——本地磁盘始终是事实源。写入后回读比对，不一致就判定失败并把
  被替换掉的旧文档写回保险库。
- **还原**：`ops 1p restore` 反向操作——私钥写回 `~/.ssh`（`0600`），明文密码写回
  `servers.yaml`（`0600`）。要写明文密码时**先**确认；非交互环境不给 `--yes` 就拒绝，且不碰
  `servers.yaml`。
- **运行时**：`servers.yaml` 里的 `op://` 引用**不支持**。残留引用会快速失败并提示先跑
  `ops 1p restore` 迁移为本地凭据。唯一的例外是 `backups.yaml` 作业的 `env` 字段：那里的
  `op://` 在运行时按需解析并注入进程环境，不落盘。
- **验证**：这条链路可以在没有 1Password 账号的机器上完整跑通，见
  [1Password 离线验证教程](../tutorial/onepassword_offline_verify.md)；`ops 1p` 各子命令的用法、
  已知边界与排障见[1Password 备份与跨机同步指南](../reference/onepassword.md)。

### 3.6 落盘敏感文件与权限

| 位置 | 权限 | 为什么敏感 |
|---|---|---|
| `servers.yaml` | `0600` | 可能含明文 SSH 密码 |
| `backups.yaml` | `0600` | 作业 `env` 中的凭据是明文 |
| `notifications.yaml` | `0600` | webhook URL 通常内嵌 token |
| `onepassword.yaml` | `0600` | 记住的保险库与账号名（不含密钥） |
| `~/.ssh/opspulse_*` | `0600` | OpsPulse 为服务器托管的私钥 |
| 数据目录与日志 | 数据目录 `0700`、日志目录 `0750`、日志文件 `0600` | 执行日志含主机名、路径与作业输出 |

OpsPulse **不加密**这些 YAML：保护手段是文件权限与目录权限，因此 `0600` 之外的任何放宽
（比如把配置目录放进同步盘或交给其它用户）都会直接扩大暴露面。目录规范的完整清单见
[配置与模板](../reference/configuration.md)。

### 3.7 明确的非承诺

不做远端最小权限隔离与审计；不做多用户或角色权限模型；不做密钥轮换自动化；不加密本机
配置文件；不提供传输层以外的额外加密。发现实现与本节描述不符，按
[SECURITY.md](../../SECURITY.md) 的渠道私密上报。

## 4. 非目标 (Non-Goals)

**不替代 Ansible 一类的配置管理。** 模板是一次性的初始化脚本，不是幂等收敛循环：没有变量
分层、没有事实采集、没有条件与 handler 语义。重复执行同一模板是否安全，取决于模板自己写得
是否幂等，而不是 OpsPulse 替你保证。

**不替代 Terraform 一类的 IaC。** 它不创建、不销毁云资源，不管理 provider 状态，也没有
plan/apply 差分。它管的是「已经存在的机器」上的清单与数据。

**不替代监控告警平台。** `ops doctor` 是一次性巡检探针（SSH 延迟、磁盘、Docker 状态），
没有时间序列存储、没有阈值规则引擎、没有值班与静默机制。它回答「现在这几台机器怎么样」，
不回答「过去一周的趋势如何」。

**不封装 restic 语义。** 不重写仓库格式、加密、快照模型与保留策略，只负责把「在哪台机器、
备什么路径、用哪个后端、保留多少份」翻译成 restic 命令，并记录执行结果。仓库里的快照如何
校验、如何 `forget`/`prune`、如何直接从仓库恢复，仍然是 restic 的能力。

## 5. 核心概念 (Core Concepts)

术语只在这里定义一次，各功能域文档只讲怎么用。

- **Asset（资产）**：服务器上一块有状态数据的稳定引用，由 `id` + `type` + `source` 描述。
  跨机还原靠它定位数据，而不是靠硬编码路径。它描述**是什么**，不描述**多久备一次**。
- **Remap（路径重映射）**：源路径前缀到目标路径前缀的映射，只在指定 `--target-server` 时
  生效，用来把 `/home/user` 这类本机路径落到目标机的 `/root` 下。
- **Snapshot（快照）**：restic 仓库里一次不可变的备份结果。OpsPulse 只保存它的 id、时间、
  大小等元数据到本机 SQLite，不复制仓库内容。
- **Job（作业）**：`backups.yaml` 里的一行，绑定「服务器 + 路径或资产 + 后端 + 保留策略 +
  环境变量」。容器直备会在备份时合成一个 Job 并登记下来。
- **Channel（通知渠道）**：`notifications.yaml` 里的一行。目前类型只有 webhook，触发条件为
  `failure` / `success` / `always`，其中 `partial` 按失败处理。
- **Bootstrap（初始化）**：用内置或自定义模板对新机器做一次性初始化（`ops bootstrap`）。
  模板是带 YAML frontmatter 的 shell 脚本。

各概念的完整字段与命令见对应的参考页：[备份、还原与调度指南](../reference/backup.md)（含调度与告警）、
[服务器与资产](../reference/server_ops.md)、[配置与模板](../reference/configuration.md)、
[1Password 备份与跨机同步指南](../reference/onepassword.md)。
