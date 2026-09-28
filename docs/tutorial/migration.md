# 跨机迁移与环境备份 (Cross-Machine Migration and Environment Backup)

这篇教程把两条链路串成一条主线：**容器跨机迁移**（把容器连配置带数据搬到另一台机器并直接跑起来）与
**WSL 环境备份还原**（把 WSL 里的开发配置备份到对象存储，再按路径重映射还原到另一台机器）。
每一步只给「照着做就能跑通」的最小命令序列，字段规范、参数表与完整 flag 清单都指向对应的参考页。

在多 VPS 环境下，Docker 容器的跨机备份与迁移往往繁琐不堪：
- 野生 `docker run` 容器没有 Compose 文件，迁移时要重新翻找历史参数；
- 运行中的数据库如果直接拷数据目录，容易因脏页引发崩溃或表损坏；
- 目标服务器往往缺少统一的启动与编排流程，需要手动编写脚本。

Ops 引入了**智能容器漂移引擎**，无论原服务是普通 Docker 容器还是 Docker Compose 编排，只需两行命令即可完成跨机无缝迁移并自动启动！

## 1. 容器跨机迁移 (Container Migration)

### 核心能力

1. **零前置配置，一步直接备份**：`ops backup run <server>:<container>`，无需预先手动登记 YAML。
2. **野生容器自动逆向**：自动抓取端口、挂载卷、环境变量与重启策略，反编译为标准现代化 `compose.yaml`。
3. **数据库无损热 Dump**：针对 MySQL / MariaDB / PostgreSQL 容器，备份前自动在容器内执行在线热导出与 gzip 实时压缩，杜绝脏页与文件锁冲突。
4. **两代 Compose 自适应**：底层自动检测并兼容 `docker compose` 与独立式 `docker-compose`。
5. **跨机还原默认直接跑起来**：`ops restore run <app> --target-server <new-vps>`，解压配置和数据后默认自动执行 `$COMPOSE up -d` 并自动等待数据库就绪后灌入数据，服务直接上线！
6. **自由改名（转正）**：支持 `--as <new-name>`，在备份或还原时轻松把临时测试名（如 `nginx-test`）换成规范名（如 `nginx`）。

### 实战场景一：野生普通容器跨机迁移并改名

假设你在 `vps-1` 上曾随手运行了一个测试容器：
```bash
docker run -d --name nginx-test -p 8080:80 -v /data/html:/usr/share/nginx/html --restart unless-stopped nginx:alpine
```

#### 第一步：一键备份并转正
```bash
# 备份 vps-1 上的 nginx-test 容器，并在生成的 Compose 和任务中重命名为 nginx
ops backup run vps-1:nginx-test --as nginx
```

> **Ops 在后台自动完成**：
> 1. SSH 连接到 `vps-1`，执行 `docker inspect nginx-test` 解析端口 `8080:80` 和挂载目录 `/data/html`。
> 2. 逆向反编译出标准化 `compose.yaml`，服务名与容器名自动命名为 `nginx`。
> 3. 将 `compose.yaml` 与 `/data/html` 中的网页数据统一打包进加密的 Restic 快照。
> 4. 自动在 `backups.yaml` 和 `assets.yaml` 中沉淀该配置。

#### 第二步：一键漂移到新 VPS 并直接启动
```bash
ops restore run nginx --target-server vps-2
```

> **执行结果**：
> 1. 数据解压到 `vps-2` 的对应目录中。
> 2. Ops 自动探测 `vps-2` 上的 Compose 引擎。
> 3. 自动执行 `docker compose up -d`。
> 4. 容器 `nginx` 已经在 `vps-2` 上顺利跑起来了！

`--target-server` 之外的还原参数（`--as`、`--no-start`、`--snapshot`、`--asset`、`--dry-run`）见
[备份、还原与调度指南](../reference/backup.md#3-还原-restore)。

### 实战场景二：数据库容器在线安全迁移 (MySQL / Postgres)

假设你在 `vps-1` 上运行着一个数据库：
```bash
docker run -d --name blog-db -e MYSQL_ROOT_PASSWORD=secret -e MYSQL_DATABASE=blog mysql:8.0
```

#### 第一步：一键备份（自动热导出）
```bash
ops backup run vps-1:blog-db
```

> **Ops 在后台自动完成**：
> 1. 自动识别该容器镜像属于 MySQL 引擎。
> 2. 自动在容器内部通过管道执行 `mysqldump --single-transaction --quick -u root $PASS --all-databases | gzip`。
> 3. 密码直接从容器内部环境变量读取，不会在宿主机 `ps aux` 进程列表中泄露明文。
> 4. 将 `.sql.gz` 纳入快照，并在备份成功后自动清理远端临时 SQL 文件。

#### 第二步：还原到新机器并自动探活灌库
```bash
ops restore run blog-db --target-server vps-2
```

> **Ops 在后台自动完成**：
> 1. 在 `vps-2` 上启动数据库容器。
> 2. **探活等待（Readiness Probe）**：自动循环检测目标容器内数据库是否就绪（`mysqladmin ping`）。
> 3. 数据库就绪后，自动将快照中的 `.sql.gz` 实时解压并灌入新容器中，数据无缝恢复！

### 实战场景三：仅恢复文件，不自动拉起容器 (`--no-start`)

如果你只是想从备份中提取历史配置文件或数据，而不希望在新机器上立刻拉起容器（例如需要先核对网络配置或修改端口）：

只需加上可选的 `--no-start` 标志：
```bash
ops restore run nginx --target-server vps-2 --no-start
```

Ops 将只解压全部配置文件和数据，不会执行 `docker compose up -d`，也不会触发数据库自动灌入。

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

- 备份作业的字段规范（`server` / `paths` / `assets` / `backend` / `retention` / `env`）与容器直备的
  行为细节见[备份、还原与调度指南](../reference/backup.md)；
- 还原的完整参数表（`--target-server`、`--as`、`--no-start`、`--snapshot`、`--asset`、`--dry-run`）
  与工作流示例见[备份、还原与调度指南](../reference/backup.md#restore-run-完整参数)；
- 所有命令与 flag 的默认值见[命令行参考](../reference/cli.md)。

## 5. 相关文档 (See Also)

- [新手入门教程](getting_started.md)：编译安装、添加服务器与首次初始化
- [备份、还原与调度指南](../reference/backup.md)：备份作业、容器直备、跨机还原与调度守护
- [1Password 备份与跨机同步指南](../reference/onepassword.md)：`op://` 凭据解析与离线校验
- [命令行参考](../reference/cli.md)：`ops` 全部命令与每个 flag 的默认值
