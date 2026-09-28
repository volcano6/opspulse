# OpsPulse

[![CI](https://github.com/volcano6/opspulse/actions/workflows/ci.yaml/badge.svg)](https://github.com/volcano6/opspulse/actions/workflows/ci.yaml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/volcano6/opspulse)](https://go.dev/)
[![License](https://img.shields.io/github/license/volcano6/opspulse)](LICENSE)

**Infrastructure Action Runner** — 面向个人开发者的自托管服务器自动化与备份编排工具。

单二进制、无服务端、无远端 Agent：一份声明式清单管理多台 VPS，用 Shell 模板做初始化，用 restic
做统一备份与跨机还原，执行历史落在本地 SQLite 里。`ops` 与 `opspulse` 是同一个二进制的两个名字，
两者完全等价。

## 为什么需要 OpsPulse (Why)

管理多台 VPS 时，个人开发者会反复撞上同样四件事：

- **重复初始化**：每开一台机器都要重敲一遍 apt、常用工具与安全配置。
- **备份散落各处**：备份脚本和 cron 分散在各台机器上，成功与否无人知晓。
- **迁移靠手抄**：换服务商时要手工导出数据、改路径、重配证书、重新上线。
- **凭据四处漂**：密码与 Token 散在明文 `.env` 里，换机器时最难搬。

OpsPulse 把这些收拢成一个可执行文件：清单、模板、备份、还原、调度、告警，都在本地跑。

## 核心特性 (Features)

1. **服务器清单与标签检索**：YAML 管理主机、标签与标签键值对，`ops ls --filter` 即筛即用。见[服务器与资产](docs/reference/server_ops.md)。
2. **无 Agent 的探测与直连**：`ops server info` 一次 SSH 采回系统、硬件与 Docker 概览；`ops ssh` 免记 IP / 端口 / 密钥，支持跳板机。见[服务器与资产](docs/reference/server_ops.md)。
3. **模板化初始化**：内置 19 个官方模板（`base`、`docker`、`security`、`restic` 等），模板是带 YAML frontmatter 的 Shell 脚本，可用同名文件覆盖。见[配置与模板](docs/reference/configuration.md#6-模板与-frontmatter-templates)。
4. **结构化备份**：`backups.yaml` 声明任务，并发可调，支持 Dry-Run 与按保留策略自动修剪；容器可免配置一键备份（自动转译 Compose、数据库在途热导）。见[备份、还原与调度指南](docs/reference/backup.md)。
5. **精准还原与跨机迁移**：按任务、快照或单个资产还原；跨机默认自动拉起容器并灌库，路径可按 `remap` 重映射。见[备份、还原与调度指南](docs/reference/backup.md#3-还原-restore)。
6. **定时调度与告警**：标准 Cron 表达式、防重叠、优雅退出；任务结束按 `failure` / `success` / `always` 推送 Webhook（Slack、Discord、飞书、钉钉、企业微信）。见[备份、还原与调度指南](docs/reference/backup.md#4-调度与守护进程-scheduler)与[配置与模板](docs/reference/configuration.md#4-通知渠道-notificationsyaml)。
7. **资产模型**：Docker Compose、Volume、数据库 Dump、Nginx 站点等有状态数据以稳定 ID 登记，备份与还原按 ID 引用。见[业务资产模型](docs/reference/server_ops.md#8-业务资产与引用保护-assets)。
8. **本地优先的凭据**：SSH 凭据只存在本机（`0600`），`ops ssh` / `ops exec` / `ops cp` 全程不碰 1Password；1Password 只作为**备份与跨机同步的目标**，`ops 1p backup` 把整机凭据写进一个 Secure Note，`ops 1p restore` 在新机器上一条命令还原。见[1Password 备份与跨机同步指南](docs/reference/onepassword.md)。

## 3 分钟上手 (Quick Start)

```bash
# 1. 添加一台服务器（密码静默输入，连通后引导注入公钥）
ops add oracle-sg ubuntu@203.0.113.10 --tags prod

# 2. 查看清单（支持按 label / tag 过滤）
ops ls

# 3. 直接登录（原生终端，支持 vim / tmux / htop）
ops ssh oracle-sg

# 4. 跑一次已定义的备份任务（任务写在 backups.yaml 里）
ops backup run web-data
```

完整路径（编译、shell 补全、`ops bootstrap` 初始化第一台机器）见[新手入门教程](docs/tutorial/getting_started.md)。

## 安装 (Install)

**下载预编译包**（无需 Go 环境）：到 [Releases](https://github.com/volcano6/opspulse/releases) 页面
下载对应平台的压缩包（linux / darwin / windows × amd64 / arm64），校验后解压即用：

```bash
# 以 linux-amd64 为例，把版本号换成实际下载的那一版
tar -xzf opspulse-v0.4.0-linux-amd64.tar.gz
grep 'linux-amd64' checksums.txt | sha256sum -c -   # macOS 用 shasum -a 256 -c -
sudo install -m 0755 opspulse /usr/local/bin/
opspulse version
```

Windows 下载 `.zip`，解压后把 `opspulse.exe` 放进 `PATH` 即可；压缩包内已含 `LICENSE` 与 `README.md`。

**从源码编译**（需 Go 1.26+）：

```bash
git clone https://github.com/volcano6/opspulse.git && cd opspulse
make install              # 编译并安装到 PATH
ops completion --install  # 注入 shell 补全（Bash / Zsh / Fish / PowerShell）
```

## 文档地图 (Docs)

- **教程**：[新手入门教程](docs/tutorial/getting_started.md)、[跨机迁移与环境备份](docs/tutorial/migration.md)、[1Password 离线验证教程](docs/tutorial/onepassword_offline_verify.md)
- **参考手册**（命令、字段、参数）：[命令行参考](docs/reference/cli.md)、[配置与模板](docs/reference/configuration.md)、[服务器与资产](docs/reference/server_ops.md)、[备份、还原与调度指南](docs/reference/backup.md)、[1Password](docs/reference/onepassword.md)
- **安全与边界**：[架构与信任模型](docs/explanation/architecture.md)、[安全策略](SECURITY.md)
- **贡献与开发**：[贡献指南](CONTRIBUTING.md)、[版本变更](CHANGELOG.md)

全部文档的索引（按场景入口、推荐阅读顺序、故障排查入口）见 [docs/README.md](docs/README.md)。

## 边界与安全模型 (Boundaries and Security)

**不做什么**：

- 不替代 Ansible 类配置管理：只按模板做一次性初始化，不做持续的状态收敛。
- 不替代 Terraform 类 IaC：管理已经存在的机器，不创建云资源。
- 不替代监控与告警平台：只在作业结束时推一条事件，不做指标采集与值班。
- 不封装 restic 的语义：仓库、快照与保留策略仍是 restic 的模型，OpsPulse 只负责编排。

**安全边界**：信任根是本机磁盘（`servers.yaml` 权限 `0600`）；主机密钥默认 fail-closed，只有显式
`OPSPULSE_TRUST_NEW_HOST_KEY=1` 才写入未知主机；私钥永不外传；交互式密码经 `0600` 受限临时文件
交给 `SSH_ASKPASS`，连接退出后写零并删除。完整的信任模型、凭据生命周期与落盘权限表见
[架构与信任模型](docs/explanation/architecture.md)；漏洞上报渠道与支持版本见 [SECURITY.md](SECURITY.md)。

## 开源协议 (License)

基于 [Apache License 2.0](LICENSE) 协议开源。
