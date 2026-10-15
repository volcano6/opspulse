# 备份、还原与调度指南 (Backup, Restore & Scheduler)

OpsPulse 通过声明式 YAML 配置统一编排多主机的数据备份任务，底层调度工业级备份工具 `restic`，并将每次备份的快照 ID、文件变更量、新增数据大小、耗时与状态持久化记录到本地 SQLite 数据库中。同一批快照既能按任务、快照或单个资产精准还原（含跨机迁移与路径重映射），也能交给内置的标准 Cron 调度引擎无人值守地定时执行，并在结束时自动联动告警。

---

## 1. 备份任务配置 `backups.yaml` (Backup Jobs)

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
      - /home/user/.config                  # 必须是绝对路径：~ 与相对路径不会被展开
    backend: /mnt/backups/local-repo
    schedule: "@daily"                      # 快捷宏调度
    env:
      RESTIC_PASSWORD: "my-local-password"
    retention:
      keep_last: 5
    description: 本地配置备份
```

### 字段详细规范

| 字段 | 类型 | 是否必填 | 说明 |
|------|------|----------|------|
| `name` | 字符串 | **是** | 备份任务的唯一标识名称 |
| `server` | 字符串 | **是** | 目标主机。可填 `servers.yaml` 中的服务器名，或填 `local`（本机执行） |
| `paths` | 字符串列表 | 否 | 待备份的文件或目录**绝对路径**列表（与 `assets` 至少填一项）。`~` 与相对路径**不会被展开**——路径会被单引号原样交给远端 shell，`~/.config` 会变成相对路径去 `<cwd>/~/.config` 找，因此加载配置时就会直接报错 |
| `assets` | 字符串列表 | 否 | 关联的业务资产 ID 列表，自动解析其 Source 路径 |
| `backend` | 字符串 | **是** | Restic 仓库地址（支持 S3、B2、Azure、SFTP、本地目录等所有 restic 支持的后端） |
| `schedule` | 字符串 | 否 | Cron 调度表达式（如 `"0 2 * * *"` 或 `"@daily"`）；字段定义、表达式示例与手动触发规则见 [调度与守护进程](#4-调度与守护进程-scheduler) |
| `env` | 键值对映射 | 否 | 运行时注入的环境变量（如 `RESTIC_PASSWORD`, `AWS_ACCESS_KEY_ID` 等） |
| `retention` | 对象 | 否 | 快照保留策略（`keep_daily`, `keep_weekly`, `keep_monthly`, `keep_yearly`, `keep_last`, `keep_tags`） |
| `remap` | 映射 | 否 | 跨机还原的路径前缀重映射（源前缀 → 目标前缀），只在 `--target-server` 还原时生效；示例见 [跨机迁移与环境备份](../tutorial/migration.md) |
| `excludes` | 字符串列表 | 否 | 排除模式匹配规则 |
| `tags` | 字符串列表 | 否 | 分类标签 |
| `description` | 字符串 | 否 | 任务描述信息 |

> **凭据安全**：`env` 中的值会以明文写入权限为 `0600` 的 `backups.yaml`，并在执行时导出为远端或本地进程环境变量。不要共享该文件；当前版本尚不支持系统 Keyring 或外部 Secret Provider。

---

## 2. 备份命令与实战 (Backup Commands)

### 查看配置的任务

```bash
ops backup list
```

### 模拟执行 (Dry Run)

在不真实连接或运行备份的情况下，预览生成的 restic 脚本与执行动作：

```bash
ops backup run web-data --dry-run
```

### 执行传统声明式任务备份

```bash
# 执行单个任务
ops backup run web-data

# 并发执行多个任务（默认并发 5；--parallel N 调整，--parallel unlimited 不设上限）
ops backup run web-data,local-configs --parallel 2

# 执行全部配置的备份任务
ops backup run all
```

### 一键容器智能备份 (无需前置配置)

无需提前编写 YAML，直接对目标 VPS 上的运行容器进行智能热备份：

```bash
# 直接备份远端 vps-01 上的 my-app 容器与挂载数据
ops backup run vps-01:my-app

# 备份时重命名（例如将测试容器 nginx-test 转换为规范的 nginx）
ops backup run vps-01:nginx-test --as nginx
```

> **注意**：这种 `ops backup run <server>:<container>` 直接备份容器的形式**没有预览模式**，
> 传 `--dry-run` 会直接报错（旧版本会忽略它并真的执行备份）。需要预览请用声明式任务：
> `ops backup run <job> --dry-run`。

> **自动处理**：
> - 自动探测是否为 Compose 项目，非 Compose 则自动逆向反编译生成 `compose.yaml`。
> - 若为 MySQL / PostgreSQL 数据库，自动执行容器内在线热 Dump 并管道压缩。
> - 自动继承默认存储仓库并将配置持久化登记至 `backups.yaml` 与 `assets.yaml`。
> - 详见 [跨机迁移与环境备份](../tutorial/migration.md)。

> **敏感环境变量确认**：野生容器的环境变量会被**原样**写进逆向生成的 `compose.yaml`，并随备份
> 进入快照。若变量名疑似凭据（含 `PASSWORD` / `PASSWD` / `SECRET` / `TOKEN` / `API_KEY` /
> `ACCESS_KEY` / `PRIVATE_KEY` / `CREDENTIAL` 等片段，或 `MYSQL_PWD` 这类复合名），命令会先打印
> **变量名清单（不回显取值）** 并要求确认，确认前不会写入任何文件：
>
> ```text
>   ⚠️  Detected 2 likely sensitive environment variable(s): API_TOKEN, MYSQL_ROOT_PASSWORD (values not shown)
>       They will be written verbatim into /var/lib/opspulse/containers/nginx/compose.yaml and stored in the backup.
> Write generated compose.yaml containing sensitive environment variable(s) API_TOKEN, MYSQL_ROOT_PASSWORD to the target? [y/N]:
> ```
>
> 拒绝、或在非交互 shell 里没有显式给 `--yes`，都会在写入前中止（此时审计记录标记为 failed）。
> 生产环境建议改用外部 secret 引用（例如 env 文件里的 `${VAR}`），而不是把凭据固化进生成的
> Compose。Compose 项目本身不逆向生成文件，因此不触发该确认。

### 远端暂存目录 `.opspulse/`

容器备份在目标机上产生的中间产物（数据库热导、命名卷归档、`manifest.yaml`）统一放在
**项目目录下的 `.opspulse/`**。项目目录对 Compose 项目就是它的工作目录（例如 `/opt/blog`），
对野生容器是 `/var/lib/opspulse/containers/<名字>`：

| 路径 | 内容 |
|------|------|
| `<项目目录>/.opspulse/dumps/<名字>.sql.gz` | 数据库热导（清单里记录为相对路径 `dumps/<名字>.sql.gz`） |
| `<项目目录>/.opspulse/volumes/<卷名>/data.tar` | 命名卷归档 |
| `<项目目录>/.opspulse/manifest.yaml` | 记录本项目包含哪些卷、热导与 compose 文件，还原时据此导入 |

单独占一个 `.opspulse/` 前缀是有意的：Compose 项目目录里的 `dumps/`、`volumes/` 往往是
**真实的数据目录**，OpsPulse 绝不能往里写、更不能整目录删除。备份开始前只清理
`.opspulse/dumps` 与 `.opspulse/volumes` 这两处 OpsPulse 自己的历史残留，备份结束后只删
`.opspulse/` 下的临时文件。

旧版本产生的快照（`manifest.yaml` 与 `volumes/` 直接落在项目目录下）**仍然可以还原**：
`ops restore run` 先找 `.opspulse/manifest.yaml`，找不到再回退到旧位置，无需手工搬运。

### 查看最新备份状态

展示所有任务的最新一次备份运行时间、状态、快照 ID、新增数据量及总容量：

```bash
ops backup status
```

### 查看历史运行记录

从 SQLite 数据库中调取指定任务的历次执行历史：

```bash
ops backup history web-data --limit 10
```

### 查询远端仓库快照

直接连接目标仓库，列出实际存储的所有快照列表：

```bash
ops backup snapshots web-data
```

### 审计记录与 `--allow-untracked`

每次真实备份都会先向本地 SQLite 写入一条 `running` 记录，结束时再更新为最终状态。默认是
**fail-closed**：只要这条记录无法创建（SQLite 被锁、目录只读、磁盘写满等），`ops backup run`
就会在**执行任何真实备份之前**直接拒绝并以非 0 退出码结束——无法留下审计痕迹的备份不予执行。

应急场景下可用 `--allow-untracked`（`ops backup run` 与 `ops daemon` 均支持）显式跳过该保护：

```bash
ops backup run web-data --allow-untracked
```

该模式下的运行会被人机两端一致地标记为「未审计」：

| 位置 | 标记方式 |
|:---|:---|
| 终端 | 运行前后打印 `UNTRACKED` 警告；批量汇总的 `STATUS` 列为 `SUCCESS (UNTRACKED)` / `FAILED (UNTRACKED)` |
| `ops backup status` / `ops backup history` | `STATUS` 列为 `SUCCESS (UNTRACKED)`；前提是最终状态能成功写回——审计库持续不可写时该次运行不会入库，终端与通知是唯一的线索 |
| 通知 | Webhook 请求体带 `"untracked": true`，`text` / `content` 标注 `UNTRACKED`；只订阅 `failure`（未写 `on` 时的默认值）的渠道同样会收到未审计的成功 |
| 退出码 | `3`（未审计成功） |

同一条 fail-closed 规则也覆盖结束阶段：运行记录已经创建、但结束时**无法写回最终状态**（同样是
锁、只读目录或磁盘错误）时，该次运行会被标记为未审计并返回退出码 `3`，不会以「正常成功」收尾
——否则记录会永远停在 `running`，而调用方以为一切正常。

退出码约定：`0` 正常成功、`3` 未审计成功（含备份成功但最终状态无法写回的情况）；其余非 0 表示
失败（通常为 `1`，远端命令失败时可能是其自身的退出码）。因此「未审计的成功」不会被脚本误判为
正常成功。

---

## 3. 还原 (Restore)

OpsPulse 提供基于 restic 快照的精准还原能力，支持同机还原、跨机迁移、单资产精准恢复与 Dry-Run 预览。每次还原操作的状态、耗时与目标信息均持久化记录到本地 SQLite 数据库中。

### 核心概念

| 概念 | 说明 |
|:---|:---|
| **同机还原** | 将快照还原到备份的原始服务器和原始路径 |
| **跨机迁移并直接启动** | 还原到新服务器后**默认自动拉起容器并自动灌库** |
| **容器改名** | 通过 `--as` 在还原启动时指定新服务/容器名 |
| **仅恢复文件** | 通过 `--no-start` 跳过容器启动和数据库灌入 |
| **路径重映射** | 通过 `--target-path` 将数据还原到不同的目录结构；任务里配置的 `remap`（源前缀 → 目标前缀）在同一次还原中一并生效 |
| **单资产精准还原** | 通过 `--asset` 仅还原指定业务资产的文件 |
| **Dry-Run 预览** | 通过 `--dry-run` 列出将被还原的文件列表，不写入任何数据 |

带 `--target-server` 的跨机还原会先把快照解压到目标机上的临时暂存目录，按 `remap` 改写路径后再
落到最终位置；`--target-path` 在此基础上再叠加一层目标前缀（默认 `/`）。完整字段与快照保留策略见
[备份任务配置](#1-备份任务配置-backupsyaml-backup-jobs)，端到端的迁移演练见
[跨机迁移与环境备份](../tutorial/migration.md)。

### CLI 命令

#### 执行还原

```bash
# 还原最新快照到原始服务器和路径
ops restore run web-data

# 跨机迁移：还原到新 VPS 并默认自动拉起容器（无需额外参数！）
ops restore run my-app --target-server new-vps

# 跨机迁移时改名
ops restore run my-app --target-server new-vps --as clean-app

# 仅恢复文件，不自动启动容器
ops restore run my-app --target-server new-vps --no-start

# 指定特定快照 ID
ops restore run web-data --snapshot abc12345

# 跨机迁移 + 路径重映射
ops restore run web-data --target-server new-vps --target-path /data/web

# 单资产精准还原（仅恢复 blog-mysql 资产路径下的文件）
ops restore run web-data --asset blog-mysql

# Dry-Run：预览文件列表
ops restore run web-data --dry-run

# 非交互执行（脚本 / CI）：跳过确认提示
ops restore run web-data --target-server new-vps --yes
```

#### 查看还原历史

```bash
# 查看所有还原历史
ops restore history

# 按任务名筛选
ops restore history web-data

# 限制显示条数
ops restore history web-data --limit 5
```

#### 失败阶段与历史标记

每次还原都会**先写入 SQLite 记录、再做校验**，所以快照、目标服务器、asset、secret 等前置校验失败同样会出现在 `ops restore history` 中。记录里的 `failure_stage` 标明失败发生在哪个阶段：

| 失败阶段 | 含义 | 历史中的状态列 |
|:---|:---|:---|
| `validation` | 前置校验失败：任务配置、容器别名、secret 解析、快照不存在或不属于该任务、目标服务器不在清单、asset 不存在。**没有触碰任何文件** | `FAILED (VALIDATION)` |
| `execution` | restic 还原命令本身失败（含 `--dry-run` 预览失败） | `FAILED (EXECUTION)` |
| `post_restore` | 文件已还原，但还原后的容器自动启动 / 灌库失败 | `PARTIAL (POST_RESTORE)` |
| （空） | 未失败：`running` / `success` / `dry-run` | `SUCCESS` / `DRY-RUN` |

失败时终端同时打印失败阶段与下一步建议：

```text
❌ Restore failed during validation: snapshot "abc123" does not belong to job "web-data"
   Next: nothing was restored; fix the reported input (see `ops backup snapshots <job>`, `ops asset list`, `ops server list`) and re-run
```

记录里还会保留这次尝试的原始参数（任务名、源/目标服务器、`--snapshot`、`--asset`、`--target-path`），即使该参数本身就是失败原因；快照解析成功后，记录里的 `snapshot_id` 会被替换为解析出的真实 ID。

### `restore run` 完整参数

| 参数 | 默认值 | 说明 |
|:---|:---|:---|
| `<job-name>` | — | 备份任务名称（必填，对应 `backups.yaml` 中的 `name`） |
| `--snapshot` | `latest` | 快照 ID，或 `latest` 自动查询最新快照 |
| `--target-server` | 与源相同 | 目标服务器名（用于跨机迁移） |
| `--target-path` | （空 = 快照中的原始绝对路径） | 还原目标路径（用于路径重映射） |
| `--as` | （原名） | 重命名还原后的容器/服务名 |
| `--no-start` | `false` | 抑制自动启动：仅解压文件，不拉起容器也不灌库 |
| `--asset` | （空=全部还原） | 指定资产 ID，仅还原该资产对应的文件 |
| `--dry-run` | `false` | 预览模式：仅列出文件，不执行实际还原（同样跳过确认提示） |
| `--yes` / `-y` | `false` | 跳过执行前的交互式确认；脚本、CI 等非交互场景必须显式传入 |

### 工作流示例

#### 场景 1：日常同机全量还原

```bash
# 查看可用快照
ops backup snapshots web-data

# 还原最新快照到原始位置
ops restore run web-data
```

#### 场景 2：VPS 到期迁移

```bash
# 1. 在旧机器上备份
ops backup run web-data

# 2. 在新机器上还原（跨机 + 路径重映射）
ops restore run web-data --target-server new-vps --target-path /opt/web

# 3. 查看还原历史确认结果
ops restore history web-data
```

#### 场景 3：精准还原单个数据库

```bash
# 仅还原 blog-mysql 资产的文件（不影响其他数据）
ops restore run web-data --asset blog-mysql

# 先预览将还原哪些文件
ops restore run web-data --asset blog-mysql --dry-run
```

### 数据持久化

每次还原操作（包括 Dry-Run）均自动记录到 SQLite 数据库中，通过 `ops restore history` 可查看：

| 字段 | 说明 |
|:---|:---|
| `ID` | 执行记录唯一 ID |
| `JOB` | 关联的备份任务名称 |
| `STATUS` | 执行状态：`SUCCESS`、`FAILED`、`DRY-RUN`，以及 `PARTIAL`（文件已还原，但随后的容器自动启动或灌库失败） |
| `SNAPSHOT` | 还原使用的快照 ID（截断为前 8 位） |
| `SOURCE` | 源服务器名称 |
| `TARGET` | 目标服务器名称（跨机时显示不同值） |
| `DURATION` | 执行耗时 |
| `STARTED AT` | 开始时间 |

`ops restore run` 在状态为 `FAILED` 或 `PARTIAL` 时以非 0 退出码结束，脚本与 CI 可直接据此判定；
`PARTIAL` 同样会触发失败类告警渠道，见[配置与模板](configuration.md#4-通知渠道-notificationsyaml)的通知渠道与 Webhook 请求体结构。

---

## 4. 调度与守护进程 (Scheduler)

OpsPulse 内置基于标准 Cron 规范的调度引擎，支持通过守护进程（Daemon）在后台自动按计划执行备份任务，并在任务执行完毕或出现故障时自动触发告警通知。

### 核心特性

- **标准 Cron 语法支持**：支持标准的 5 位 Cron 表达式（分、时、日、月、周）与常用快捷宏（如 `@daily`、`@hourly`、`@weekly`、`@every 2h`）。
- **防重叠并发保护 (Skip-If-Running)**：当上一次备份任务耗时较长、下一次调度触发时刻已到时，调度器会自动跳过本次执行，防止多实例重复争抢远端带宽或磁盘 I/O。
- **告警自动联动**：任务执行结束后，调度器会自动收集备份状态、快照 ID、文件变更量与耗时，通过 `internal/notify` 自动分发到配置的 Webhook 渠道（如仅在失败时告警）。
- **优雅退出 (Graceful Shutdown)**：响应系统 `SIGINT` / `SIGTERM` 信号，等待正在执行中的备份任务完成并安全落库后退出（超时 30 秒兜底保护）。
- **单次批量执行模式 (`--once`)**：支持一次性按序执行全部已配置调度的备份任务并立即退出，完美适配 Linux 宿主系统的外部 `crontab` 或 `systemd timer`。

### 任务调度配置 (`backups.yaml`)

`schedule` 是 `backups.yaml` 中每个备份任务的调度字段（字符串，可选）：填标准 5 位 Cron 表达式或快捷宏，
未配置或留空的任务不会被调度器自动执行。在 `$XDG_CONFIG_HOME/opspulse/backups.yaml` 中为指定备份任务增加该字段：

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
      - /home/user/.config                  # 绝对路径（~ 不会被展开）
    backend: /mnt/backups/local-repo
    schedule: "@daily"
    env:
      RESTIC_PASSWORD: "local-password"
```

#### 常用 Schedule 表达式示例

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
> 如果某个备份任务未配置 `schedule` 字段或留空，该任务将仅支持手动通过 `ops backup run <name>` 触发，不会被调度器自动执行。

`schedule` 只是 `backups.yaml` 的一个字段，其余字段（`paths`、`backend`、`retention`、`remap` 等）见
[备份任务配置](#1-备份任务配置-backupsyaml-backup-jobs)。防重叠靠的是调度器进程内的「上一次还没跑完就跳过」判断，
不落锁文件，取舍见[架构与信任模型](../explanation/architecture.md)；任务跑完后怎么发告警见
[配置与模板](configuration.md#4-通知渠道-notificationsyaml)。

### CLI 运行方式

#### 前台运行守护进程

```bash
ops daemon
```

输出示例：

```text
[scheduler] 📋 Registered 2 scheduled backup job(s):
  - web-data         [0 2 * * *] next: 2026-09-04 02:00:00
  - db-backup        [0 */6 * * *] next: 2026-09-03 20:00:00
[scheduler] 🚀 Daemon started. Waiting for schedule triggers (press Ctrl+C to stop)...
```

#### 单次执行所有调度任务 (`--once`)

适合由系统自带的 cron 调度，或者在维护时手动执行一次全部定时任务：

```bash
ops daemon --once
```

`--once` 与常驻 daemon 使用相同的 schedule 校验：留空的任务不执行，非法表达式会显示任务名、原始表达式和修复提示并跳过；如果所有已配置的 schedule 都非法，命令返回非零且不会执行任何备份。至少保留一个合法任务时，合法任务继续按序执行。

#### 审计失败时的行为 (`--allow-untracked`)

daemon 与 `ops backup run` 一样默认 fail-closed：运行记录无法写入 SQLite 时，该次备份**不会执行**，daemon 会把该次作业记为失败并写入日志（`[scheduler] ... finished with error`）。常驻模式下进程本身不会因此退出，只有 `ops daemon --once` 会以非 0 退出码结束。

```bash
ops daemon --allow-untracked
```

应急模式下备份照常执行，但结果会同时：
- 在通知中带上 `"untracked": true`，并标注 `UNTRACKED`；
- `ops daemon --once` 下以退出码 `3` 结束（区别于正常退出与普通失败）。

也就是说，未审计的成功在守护进程路径上也不会被当成正常成功。常驻 daemon 不返回退出码，因此
长期运行时只能靠通知或 `[scheduler] ... finished with error` 日志发现——需要脚本判定退出码时
请用 `ops daemon --once`。

### 生产环境部署 (Systemd)

在生产 VPS 或管理机上，推荐将 Ops 注册为 systemd 服务长期保持后台运行：

创建 `/etc/systemd/system/ops.service`：

```ini
[Unit]
Description=Ops Automated Backup Scheduler Daemon
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
# 指定执行用户与环境路径（如需自定义配置目录可注入 OPSPULSE_HOME）
Environment="PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin"
ExecStart=/usr/local/bin/ops daemon
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
systemctl enable --now ops.service
systemctl status ops.service
```

---

## 5. Docker 服务发现与迁移能力 (`docker list` / `docker inspect`)

在真正备份或迁移容器之前，先用这组**只读**命令看清 VPS 上有什么、每个容器能不能迁。
它们只执行 `docker ps` / `docker inspect` 与一次环境能力探测，**不会**执行数据库 dump、
volume 归档、restic 备份或 `compose up`，也不会修改远端任何文件。

### 列出容器 (`ops docker list`)

```bash
ops docker list vps-01          # 只列运行中的容器
ops docker list vps-01 --all    # 连已停止的一起列
ops docker list vps-01 --json   # 结构化输出，便于脚本处理
```

表格给出容器名、镜像、编排方式（`compose:<项目>` 或 `run`）、识别到的数据库类型、状态与
端口。`--json` 输出 `{"server": "...", "containers": [...]}`。

### 单个容器的迁移分析 (`ops docker inspect`)

```bash
ops docker inspect vps-01 blog-db
ops docker inspect vps-01 blog-db --json
```

分析内容：

- 编排方式：Docker Compose 项目/服务，或普通 `docker run` 容器；
- 挂载：named volume 与 bind mount 分别列出；
- 数据库：识别 MySQL / MariaDB / Percona / PostgreSQL，给出逻辑导出策略与数据目录；
- 外部宿主机依赖：bind mount、系统挂载、设备、自定义网络；
- 疑似敏感环境变量：**只列变量名，不回显取值**；
- 各 Docker 属性的支持级别：`supported` / `partial` / `unsupported`；
- 远端能力：Docker / Compose / restic / helper 镜像 / registry 可达性 / 磁盘空间，以及容器
  用到的宿主机路径是否可写；缺 Docker、Compose、restic、helper 镜像或磁盘空间不足时给出明确提示。

`--json` 输出结构化结果，环境变量同样只含名字（`env_keys`），不含取值。

> 这组命令只负责「看清现状」，不负责备份或迁移；真正执行仍走
> [一键容器智能备份](#一键容器智能备份-无需前置配置) 与 [还原](#3-还原-restore)。
> Docker 属性的完整支持边界矩阵由后续文档统一维护，本节只说明命令本身的输出。

### 只读迁移 plan (`ops docker plan`)

在真正备份或迁移之前，为一个容器（或**显式**选择的一组容器）生成可审查、可保存的 plan：

```bash
ops docker plan vps-01 blog-db                  # 单容器 plan：默认保存为 YAML 并打印路径
ops docker plan vps-01 blog-db --target vps-02  # 指定目标机：额外检查目标机能力
ops docker plan vps-01 blog-db --save migration-plan.yaml
ops docker plan vps-01 --all                    # 显式批量：该服务器上所有运行中的容器
ops docker plan vps-01 --all --json             # 结构化输出，便于脚本处理
ops docker plan show migration-plan.yaml        # 重新读取已保存的 plan
```

默认只处理**显式指定**的容器：不写容器名时必须加 `--all`，否则命令直接报错——不会因为漏参数
就把整台 VPS 的服务全部纳入 plan。

plan 记录的内容：

- 源与目标（未指定目标时按源机原样记录路径与端口，并明确标注「未指定」）；
- 服务名/容器名、编排方式（`compose:<项目>` 或 `run`）；
- 配置：Compose 文件、项目目录、重启策略、端口、环境变量名（**只列名字**）与疑似敏感变量；
- named volume、bind mount、数据库引擎与导出策略（`logical_dump` / `file_level`）、一致性策略；
- 镜像获取方式：`pull_from_registry` / `preload_required` / `unknown`；
- 目标路径与目标端口（可在 plan 文件中手工编辑，执行前会重新校验）；
- 每个 Docker 属性的支持级别（`supported` / `partial` / `unsupported`，与 `ops docker inspect`
  同一张表）。

风险分三档，plan 与 `--json` 输出都按这三档分组：

- **hard blocker**：阻止执行。例如目标机缺 Docker / Compose / restic / helper 镜像、磁盘空间不足、
  必需路径无法创建、容器本身无法按单容器迁移。
- **warning**：可见、可确认继续。例如外部 bind mount、宿主机设备、自定义网络、特权模式、敏感环境
  变量、镜像必须预置、非受支持数据库只能文件级复制。
- **informational follow-up**：人工后续步骤。例如在目标机创建目录/网络、预置镜像、迁移后逐项核对属性。

plan 严格只读：不写入备份配置、不生成快照、不启动服务、不覆盖文件。保存 plan 时若同名文件已存在会
直接拒绝覆盖；`--no-save` 可完全关闭落盘。默认保存位置是 OpsPulse 配置目录下的
`plans/<时间戳>-<服务器>-<标签>.yaml`，可用 `ops docker plan show <file>` 重新读取（文件带
`format_version`，版本不匹配会明确报错）。

写入备份配置必须显式加 `--write-config`：命令先打印变更摘要（新增还是更新、作业名、server、paths、
backend）与敏感环境变量风险，只有在交互确认或显式 `--yes` 之后才会写入 `backups.yaml`；非交互环境
下没有 `--yes` 会直接拒绝。没有可继承的 restic 仓库时同样拒绝写入。

---

## 6. 相关文档 (See Also)

- [命令行参考](cli.md)：`ops` 全部命令与每个 flag 的权威清单（生成物）。
- [配置与模板](configuration.md)：配置与数据目录位置、`servers.yaml` / `assets.yaml` / `notifications.yaml` 字段、Webhook payload 结构与 SQLite 落盘位置。
- [架构与信任模型](../explanation/architecture.md)：执行模型、防重叠与信任边界。
- [跨机迁移与环境备份](../tutorial/migration.md)：容器跨机迁移与 WSL 环境备份还原的端到端教程。
