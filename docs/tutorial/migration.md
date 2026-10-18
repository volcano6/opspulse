# 跨机迁移与环境备份 (Cross-Machine Migration and Environment Backup)

这篇教程把两条链路串成一条主线：**容器跨机迁移**（把容器连配置带数据搬到另一台机器并直接跑起来）与
**WSL 环境备份还原**（把 WSL 里的开发配置备份到对象存储，再按路径重映射还原到另一台机器）。
每一步只给「照着做就能跑通」的最小命令序列，字段规范、参数表与完整 flag 清单都指向对应的参考页。

> **范围**：这里支持的是常见单主机 Docker 服务迁移，**不保证任意 Docker workload 无损迁移**。
> Compose、普通单容器、named volume、常规 bind mount 与 MySQL/MariaDB/Percona/PostgreSQL
> 的逻辑导出在支持范围内；任意 runtime 字段、离线镜像、任意数据库、GPU/宿主机资源、
> DNS/证书/反向代理与完整手写 SSH 配置需要人工处理。完整矩阵与前置条件见
> [Docker 支持边界矩阵](../reference/backup.md#docker-支持边界矩阵)。

在多 VPS 环境下，Docker 容器的跨机备份与迁移往往繁琐不堪：
- 野生 `docker run` 容器没有 Compose 文件，迁移时要重新翻找历史参数；
- 运行中的数据库如果直接拷数据目录，容易因脏页引发崩溃或表损坏；
- 目标服务器往往缺少统一的启动与编排流程，需要手动编写脚本。

OpsPulse 把这件事拆成可检查的步骤：先发现、再生成只读 plan、复核后执行，最后单独做健康检查。
plan 不是绕过安全检查的许可；apply 会重新校验源端漂移、目标冲突、端口、目录与权限、镜像和数据库条件。

## 1. 容器跨机迁移 (Container Migration)

### 核心能力

1. **先发现，再执行**：`ops docker list` / `ops docker inspect` 只读展示容器、挂载、数据库与能力缺口。
2. **可审查的 plan**：`ops docker plan` 记录源与目标、端口、路径、镜像获取方式、数据库策略和风险。
3. **执行前重新校验**：`ops docker apply` 不接受过期 plan，目标冲突或源容器漂移会阻止执行。
4. **受支持数据库使用逻辑导出**：MySQL / MariaDB / Percona 走 `mysqldump`，PostgreSQL 走
   `pg_dumpall`；其他数据库只能文件级复制，不承诺一致性。
5. **named volume 与常规 bind mount 分别处理**：named volume 可归档导入；bind mount 依赖目标机
   预先准备目录与权限。
6. **启动后健康检查**：`ops docker health` 复核容器、固定端口、数据库 readiness 与可选 HTTP。
7. **默认保留名称**：Docker plan/apply 默认保留服务名与项目名，避免与反向代理或运维引用失配；
   `--as <new-name>` 只用于 `ops backup run` / `ops restore run` 的别名流程。

### 最小可跑流程：发现 → plan → apply → 健康检查

以下示例使用中性服务器名与保留地址。执行前请确认源机与目标机都已登记在 `servers.yaml`，
目标机能访问镜像仓库（或已预置镜像），需要 named volume 时两端都有 helper 镜像。

```bash
# 1. 发现：列出源机容器，查看目标容器的迁移分析
ops docker list vps-01
ops docker inspect vps-01 blog-web

# 2. plan：指定目标机，记录可选的 HTTP 健康检查地址
ops docker plan vps-01 blog-web \
  --target vps-02 \
  --health-url http://vps-02:8080/health \
  --save migration-plan.yaml

# 3. 复核 plan；--dry-run 只做 apply 预检，不执行迁移
ops docker plan show migration-plan.yaml
ops docker apply migration-plan.yaml --dry-run

# 4. 执行：备份 → 恢复 → 启动；交互确认或显式 --yes
ops docker apply migration-plan.yaml

# 5. 独立复核健康状态（只读）
ops docker health migration-plan.yaml
```

若目标机已有同名容器、同名 Compose 项目、已占用端口或已存在的项目目录，apply 会阻止执行，
默认不会自动改名。需要调整端口或 bind mount 路径时，编辑 plan 中的 `target_ports` /
`target_paths` 后重新预检；apply 会重写目标机 Compose 文件，注释与格式会丢失。

### 关于改名：`--as` 的适用范围

`--as` 是 `ops backup run` / `ops restore run` 的作业与容器别名，不是
`ops docker apply` 的改名开关。需要直接复用备份链路改名时，可以这样执行：

```bash
ops backup run vps-01:blog-web-test --as blog-web
ops restore run blog-web --target-server vps-02 --start
```

`ops docker plan` 记录的服务名来自被检查的容器；`ops docker apply` 按 plan 中的服务名备份、
恢复和启动，不会继承此前单独执行 `ops backup run --as` 创建的作业别名。若使用 plan 迁移，
请先确认计划中的服务名和目标冲突；目标机仍需没有同名容器、项目、端口和目录。若容器含疑似敏感
环境变量，普通容器反向生成 `compose.yaml` 前会要求确认，拒绝或非交互环境未给 `--yes` 时会在
写盘前中止。

### 实战场景：受支持数据库容器迁移

MySQL / MariaDB / Percona 与 PostgreSQL 容器会使用逻辑导出。迁移前必须确认容器内存在
`mysqldump` / `pg_dumpall` 及 readiness 工具，且数据库凭据能从容器环境变量读取。

```bash
ops docker inspect vps-01 blog-db
ops docker plan vps-01 blog-db --target vps-02 --save migration-plan.yaml
ops docker apply migration-plan.yaml --dry-run
ops docker apply migration-plan.yaml
ops docker health migration-plan.yaml
```

数据库导入失败时，恢复结果会把 `database` 步骤标为 `failed` 或 `blocked`，Compose 启动也可能
已完成；这不是“整体成功”，需要按汇总中的下一步命令人工处理。数据库自身的一致性、版本兼容与
应用层数据修复不在自动保证范围内。

### 恢复默认启动行为与 `--no-start` / `--start`

`ops docker apply` 由 plan 驱动，按 plan 备份、恢复并启动目标服务。若需要单独复用已有快照，
`ops restore run` 的启动策略如下：

| 场景 | 默认行为 |
|:---|:---|
| 普通文件恢复（快照中没有匹配的容器 manifest） | 只写回文件，不启动服务 |
| 同机恢复（目标服务器 = 源服务器） | 只写回文件，不启动服务 |
| 跨机恢复且找到与本任务匹配的容器 manifest | 导入 named volume → Compose 启动 → 导入数据库 |

显式覆盖：

```bash
# 只解压文件：不导入 named volume、不灌库、不启动 Compose
ops restore run blog-web --target-server vps-02 --no-start

# 强制启动：普通文件恢复或同机恢复也执行后续容器步骤
ops restore run blog-web --start
```

`--start` 与 `--no-start` 互斥。恢复结果按 `files` / `volumes` / `database` / `compose`
四步展示；聚合状态为 `success` / `partial` / `failed` / `dry-run`，`failed` 或 `partial`
都会以非零退出码结束，并发送 `restore_failed` / `restore_partial` 通知。`partial` 表示文件已
恢复，但后续步骤失败或被阻止，不能当作完整迁移成功。

`ops restore run` **不会自动执行迁移后健康检查**；健康检查由 `ops docker apply` 默认执行，
或对保存的 plan 单独运行 `ops docker health`。

## 2. WSL 环境备份与还原 (WSL Environment Backup and Restore)

这条链路把 WSL 里的开发配置备份到对象存储，再按路径重映射还原到另一台机器；凭据边界与信任模型见
[架构与信任模型](../explanation/architecture.md)。

### 备份 WSL 环境配置 (Back Up a WSL Environment)

新建 `$XDG_CONFIG_HOME/opspulse/backups.yaml`（默认 `~/.config/opspulse/backups.yaml`）：

```yaml
backups:
  - name: my-wsl-env
    server: local                      # 在运行 OpsPulse 的这台机器上执行
    backend: s3:s3.amazonaws.com/my-bucket/configs
    env:
      # 运行时由 OpsPulse 调用 1Password CLI 解析，绝不落盘
      RESTIC_PASSWORD: "op://Personal/Restic/password"
      AWS_ACCESS_KEY_ID: "op://Personal/AWS/username"
      AWS_SECRET_ACCESS_KEY: "op://Personal/AWS/credential"
    # paths 必须是绝对路径：~ 与相对路径不会被展开，加载配置时直接报错。
    # 下面用 /home/user 作占位，请换成你的家目录（echo $HOME 查看）。
    paths:
      - /home/user/.zshrc.local
      - /home/user/.gitconfig
    remap:
      "/home/user": "/root"            # 为跨机还原做准备：WSL 家目录 → 目标机家目录
    retention:
      keep_last: 5
```

`assets`、`schedule`、`excludes`、`tags` 等其余字段见
[备份、还原与调度指南](../reference/backup.md#字段详细规范)；`env` 里 `op://` 的解析规则见
[1Password 备份与跨机同步指南](../reference/onepassword.md#仍然保留backupsyaml-里的-env-op)。

执行备份：

```bash
ops backup run my-wsl-env
```

如果本机没有安装 1Password CLI，也可以用官方 `op run` 注入环境变量，效果等价：

```bash
op run --env-file=~/.env.op -- ops backup run my-wsl-env
```

### 跨机无损还原 (Cross-Machine Restore with Path Remapping)

WSL 里备份的路径是 `/home/user/...`，而目标 VPS 的家目录是 `/root`。直接还原会在 VPS 上造出一层
多余的 `/root/home/user/`，`remap` 会在暂存目录里把源前缀换成目标前缀：

```bash
# 目标机需已登记在 servers.yaml 中（见新手入门教程第 4 步）
ops restore run my-wsl-env --target-server vps-01
```

- `/home/user/.zshrc.local` 会被还原到 `vps-01` 的 `/root/.zshrc.local`；
- 路径替换全自动完成，不需要手工 `mv`。

`remap` 只在带 `--target-server` 的还原里生效，同机还原按原始路径写回；还原的完整参数表与工作流示例
统一列在本文第 4 节的速查里。

### 推荐备份的开发环境清单 (Recommended Items)

- Shell: `~/.zshrc`、`~/.bashrc`、`~/.profile`
- Git: `~/.gitconfig`、`~/.gitignore_global`
- SSH: `~/.ssh/config`、`~/.ssh/known_hosts`（私钥不要备份到对象存储，改用 `ops 1p backup`
  存进 1Password，见[1Password 备份与跨机同步指南](../reference/onepassword.md)）
- 命令行工具: `~/.aws/config`、`~/.kube/config`、`~/.config/gh/`
- 编辑器: `~/.config/nvim/`

## 3. 远端暂存目录与旧快照 (Remote Staging and Old Snapshots)

备份期间在目标机上产生的中间产物（数据库热导、命名卷归档、`manifest.yaml`）统一放在**项目目录下的
`.opspulse/`**，单独占这个前缀是为了不和项目里真实存在的 `dumps/`、`volumes/` 混在一起：每次备份
只清理 `.opspulse/` 下的历史残留，绝不碰项目自身的目录。完整目录清单，以及旧版本快照
（`manifest.yaml` 直接落在项目目录下）如何自动回退兼容，见
[备份、还原与调度指南](../reference/backup.md#远端暂存目录-opspulse)。

## 4. 常用命令速查 (Command Cheatsheet)

场景里用到的两条命令是 `ops backup run` 与 `ops restore run`：

- Docker 发现、plan、apply 与健康检查的入口分别是 `ops docker list`、`ops docker plan`、
  `ops docker apply` 与 `ops docker health`；支持矩阵见
  [Docker 支持边界矩阵](../reference/backup.md#docker-支持边界矩阵)。
- 备份作业的字段规范（`server` / `paths` / `assets` / `backend` / `retention` / `env`）与容器直备的
  行为细节见[备份、还原与调度指南](../reference/backup.md)；
- 还原的完整参数表（`--target-server`、`--as`、`--start`、`--no-start`、`--snapshot`、`--asset`、`--dry-run`）
  与工作流示例见[备份、还原与调度指南](../reference/backup.md#restore-run-完整参数)；
- 所有命令与 flag 的默认值见[命令行参考](../reference/cli.md)。

## 5. 相关文档 (See Also)

- [新手入门教程](getting_started.md)：编译安装、添加服务器与首次初始化
- [备份、还原与调度指南](../reference/backup.md)：备份作业、容器直备、跨机还原与调度守护
- [1Password 备份与跨机同步指南](../reference/onepassword.md)：`op://` 凭据解析与离线校验
- [命令行参考](../reference/cli.md)：`ops` 全部命令与每个 flag 的默认值
