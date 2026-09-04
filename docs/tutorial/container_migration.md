# 容器服务极速备份与跨 VPS 无缝迁移实战指南

在多 VPS 环境下，Docker 容器的跨机备份与迁移往往繁琐不堪：
- 野生 `docker run` 容器没有 Compose 文件，迁移时要重新翻找历史参数；
- 运行中的数据库如果直接拷数据目录，容易因脏页引发崩溃或表损坏；
- 目标服务器往往缺少统一的启动与编排流程，需要手动编写脚本。

OpsPulse v0.6 引入了**智能容器漂移引擎**，无论原服务是普通 Docker 容器还是 Docker Compose 编排，只需两行命令即可完成跨机无缝迁移并自动启动！

---

## 核心能力

1. **零前置配置，一步直接备份**：`opspulse backup run <server>:<container>`，无需预先手动登记 YAML。
2. **野生容器自动逆向**：自动抓取端口、挂载卷、环境变量与重启策略，反编译为标准现代化 `compose.yaml`。
3. **数据库无损热 Dump**：针对 MySQL / MariaDB / PostgreSQL 容器，备份前自动在容器内执行在线热导出与 gzip 实时压缩，杜绝脏页与文件锁冲突。
4. **两代 Compose 自适应**：底层自动检测并兼容 `docker compose` 与独立式 `docker-compose`。
5. **跨机还原默认直接跑起来**：`opspulse restore run <app> --target-server <new-vps>`，解压配置和数据后默认自动执行 `$COMPOSE up -d` 并自动等待数据库就绪后灌入数据，服务直接上线！
6. **自由改名（转正）**：支持 `--as <new-name>`，在备份或还原时轻松把临时测试名（如 `nginx-test`）换成规范名（如 `nginx`）。

---

## 实战场景一：野生普通容器跨机迁移并改名

假设你在 `vps-1` 上曾随手运行了一个测试容器：
```bash
docker run -d --name nginx-test -p 8080:80 -v /data/html:/usr/share/nginx/html --restart unless-stopped nginx:alpine
```

### 第一步：在 OpsPulse 中一键备份并转正
```bash
# 备份 vps-1 上的 nginx-test 容器，并在生成的 Compose 和任务中重命名为 nginx
opspulse backup run vps-1:nginx-test --as nginx
```

> **OpsPulse 在后台自动完成**：
> 1. SSH 连接到 `vps-1`，执行 `docker inspect nginx-test` 解析端口 `8080:80` 和挂载目录 `/data/html`。
> 2. 逆向反编译出标准化 `compose.yaml`，服务名与容器名自动命名为 `nginx`。
> 3. 将 `compose.yaml` 与 `/data/html` 中的网页数据统一打包进加密的 Restic 快照。
> 4. 自动在 `backups.yaml` 和 `assets.yaml` 中沉淀该配置。

### 第二步：一键漂移到新 VPS 并直接启动
```bash
opspulse restore run nginx --target-server vps-2
```

> **执行结果**：
> 1. 数据解压到 `vps-2` 的对应目录中。
> 2. OpsPulse 自动探测 `vps-2` 上的 Compose 引擎。
> 3. 自动执行 `docker compose up -d`。
> 4. 容器 `nginx` 已经在 `vps-2` 上顺利跑起来了！

---

## 实战场景二：数据库容器在线安全迁移 (MySQL / Postgres)

假设你在 `vps-1` 上运行着一个数据库：
```bash
docker run -d --name blog-db -e MYSQL_ROOT_PASSWORD=secret -e MYSQL_DATABASE=blog mysql:8.0
```

### 第一步：一键备份（自动热导出）
```bash
opspulse backup run vps-1:blog-db
```

> **OpsPulse 在后台自动完成**：
> 1. 自动识别该容器镜像属于 MySQL 引擎。
> 2. 自动在容器内部通过管道执行 `mysqldump --single-transaction --quick -u root $PASS --all-databases | gzip`。
> 3. 密码直接从容器内部环境变量读取，不会在宿主机 `ps aux` 进程列表中泄露明文。
> 4. 将 `.sql.gz` 纳入快照，并在备份成功后自动清理远端临时 SQL 文件。

### 第二步：还原到新机器并自动探活灌库
```bash
opspulse restore run blog-db --target-server vps-2
```

> **OpsPulse 在后台自动完成**：
> 1. 在 `vps-2` 上启动数据库容器。
> 2. **探活等待（Readiness Probe）**：自动循环检测目标容器内数据库是否就绪（`mysqladmin ping`）。
> 3. 数据库就绪后，自动将快照中的 `.sql.gz` 实时解压并灌入新容器中，数据无缝恢复！

---

## 实战场景三：仅恢复文件，不自动拉起容器 (`--no-start`)

如果你只是想从备份中提取历史配置文件或数据，而不希望在新机器上立刻拉起容器（例如需要先核对网络配置或修改端口）：

只需加上可选的 `--no-start` 标志：
```bash
opspulse restore run nginx --target-server vps-2 --no-start
```

OpsPulse 将只解压全部配置文件和数据，不会执行 `docker compose up -d`，也不会触发数据库自动灌入。

---

## 常用命令速查

| 操作 | 命令行示例 | 说明 |
| :--- | :--- | :--- |
| **容器备份** | `opspulse backup run vps-1:my-app` | 自动识别并全量打包容器配置与数据 |
| **备份并重命名** | `opspulse backup run vps-1:web-dev --as web-prod` | 生成的 Compose 与任务直接改名为新名称 |
| **跨机还原与启动** | `opspulse restore run my-app --target-server vps-2` | 还原并默认自动拉起容器与灌库 |
| **还原时改名** | `opspulse restore run my-app --target-server vps-2 --as new-app` | 还原到目标 VPS 时指定新项目名启动 |
| **仅恢复文件** | `opspulse restore run my-app --target-server vps-2 --no-start` | 仅解压数据，不启动容器 |
| **指定快照还原** | `opspulse restore run my-app --target-server vps-2 --snapshot <id>` | 还原到指定的历史版本快照 |
