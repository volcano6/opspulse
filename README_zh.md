# OpsPulse

[![CI](https://github.com/volcano6/opspulse/actions/workflows/ci.yaml/badge.svg)](https://github.com/volcano6/opspulse/actions/workflows/ci.yaml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/volcano6/opspulse)](https://go.dev/)
[![License](https://img.shields.io/github/license/volcano6/opspulse)](LICENSE)

[English](README.md) | 简体中文

**Infrastructure Action Runner** — 面向个人开发者的自托管服务器运维编排、环境自举与结构化备份 CLI 工具。

> **核心定位**：个人 WSL / Linux 用户管理 10–20 台 VPS 实例的单二进制运维工具。
> **明确拒绝**：无 Web 控制台、无企业级 PaaS 冗余、无多用户 RBAC、无 Kubernetes/Swarm 编排。
> **设计哲学**：本地优先、远端免装 Agent、默认 Fail-Closed 安全防呆。

---

## 为什么需要 OpsPulse (Why)

管理多台云 VPS（如 Oracle Cloud、RackNerd、搬瓦工、轻量云等）时，个人开发者与自动化脚本常常反复遭遇同样的运维痛点：

- **重复初始化**：每开一台新机都要重敲一遍包管理器更新、常用工具与系统安全加固。
- **备份散落各处**：各主机的备份脚本和 cron 缺乏集中管理，任务是否成功、数据是否损坏无人知晓。
- **凭据四处飘散**：密码与私钥散落在各个明文配置中，换新电脑或重装系统时迁移难度极高。

OpsPulse 将这些责任收拢为一个单二进制命令行工具，全部逻辑在本地受控运行。

---

## 当前已就绪的核心特性 (Core Capabilities)

### 1. 服务器清单与无 Agent 直连
- **声明式清单管理**：通过规范的 YAML 格式（`servers.yaml`）管理服务器、SSH 凭据、跳板机（Bastion）与标签分组。
- **一次探测全貌**：`ops server info <name>` 一次 SSH 往返采回系统概览、硬件规格与 Docker 运行时信息，秒级格式化展示。
- **原生终端直连**：`ops ssh <name>` 直接拉起系统原生终端会话，自动读取清单参数，无需记忆 IP、端口或密钥路径。

### 2. 内置 Shell 模板一键自举 (Bootstrap)
- **19 个官方自举脚本**：支持使用带 YAML frontmatter 的成熟 Shell 模板一键初始化新机（`base`、`docker`、`security`、`restic` 等）。
- **本地配置覆盖**：在本地配置目录放置同名脚本，即可直接覆盖官方默认模板。

### 3. 基于 restic 的结构化备份与精准还原
- **声明式 restic 增量备份**：在 `backups.yaml` 中声明备份作业。支持并发调节、Dry-Run 预演、自动快照保留策略与本地 SQLite 审计记录。
- **灾难应急模式（`--allow-untracked`）**：默认 Fail-Closed 保护审计完整性。在本地 SQLite 损坏或文件锁死等极端灾难场景下，支持 `--allow-untracked` 紧急推进数据还原（退出码为 `3`），绝不将运维人员关在门外。
- **业务资产模型**：在 `assets.yaml` 中将 Docker Compose、Volume、数据库 Dump 登记为独立资产，备份与恢复支持按资产 ID 精准定位。

### 4. 本地优先凭据与 1Password 灾备
- **热路径零第三方依赖**：日常连接与执行（`ops ssh`、`ops exec`、`ops cp`）直接读取本地私钥（严格 `0600` 权限），不依赖网络服务。
- **一键新机灾难重建**：`ops 1p backup` 将整机清单与托管私钥安全备份至 1Password 加密安全笔记；换新电脑后只需执行 `ops 1p restore`，数分钟内全链路恢复。

---

## 3 分钟上手 (Quick Start)

```bash
# 1. 添加一台服务器（交互式指纹确认，静默输入密码，引导公钥注入）
ops add vps-01 203.0.113.10 --tags prod

# 2. 采集机器与 Docker 运行状态
ops server info vps-01

# 3. 直连原生交互式终端
ops ssh vps-01

# 4. 执行一次声明式备份任务
ops backup run web-data
```

完整流程（从源码编译、Shell 补全到多机编排）见 [新手入门教程](docs/tutorial/getting_started.md)。

---

## 安装 (Install)

### 下载预编译包（推荐）
在 [Releases](https://github.com/volcano6/opspulse/releases) 页面下载对应平台的压缩包（linux / darwin / windows × amd64 / arm64）：

```bash
# 以 linux-amd64 为例（把 v0.4.0 换成最新 Release 的版本号）：
tar -xzf opspulse-v0.4.0-linux-amd64.tar.gz
grep 'linux-amd64' checksums.txt | sha256sum -c -
sudo install -m 0755 opspulse /usr/local/bin/ops
ops version
```

每个官方 Release 均附带 SPDX JSON SBOM 并通过 GitHub Build Provenance 签名。可用 GitHub CLI 核验：
```bash
gh attestation verify opspulse-v0.4.0-linux-amd64.tar.gz --repo volcano6/opspulse
```

### 从源码编译（需 Go 1.26+）
```bash
git clone https://github.com/volcano6/opspulse.git && cd opspulse
make install              # 编译并安装 ops 到 PATH
ops completion --install  # 自动注册 Shell 补全（Bash / Zsh / Fish / PowerShell）
```

---

## 演进里程碑 (Roadmap)

- **v0.5.0（下一版本）**：发布加固、扩大迁移测试覆盖面与日常运维打磨。
- **v0.6.0**：架构模块化解耦（`ssh.go` 拆分）与守护进程系统服务化（systemd / Windows 计划任务）。
- **v0.7.0**：WSL 深度集成优化、SSH 拓扑秒级重建与本地缓存状态快览。
- **v0.8.0**：在现有 `plan` / `apply` 流程之上扩大 Docker 迁移覆盖面并加固预检。

---

## 文档地图 (Docs)

- **教程**：[新手入门教程](docs/tutorial/getting_started.md)、[容器跨机迁移与环境备份](docs/tutorial/migration.md)、[1Password 离线验证教程](docs/tutorial/onepassword_offline_verify.md)
- **参考手册**：[命令行参考](docs/reference/cli.md)、[备份、还原与调度指南](docs/reference/backup.md)、[配置与模板](docs/reference/configuration.md)、[服务器与资产](docs/reference/server_ops.md)、[1Password](docs/reference/onepassword.md)
- **架构与边界**：[架构与信任模型](docs/explanation/architecture.md)、[安全策略](SECURITY.md)
- **规范与开发**：[变更日志](CHANGELOG.md)、[贡献指南](CONTRIBUTING.md)

---

## 边界与非目标 (Boundaries)

- **不替代 Ansible/Puppet**：只按模板做单次初始化与命令执行，不做持续的状态收敛。
- **不替代 Terraform**：只管理已有服务器与资产，不调用云 API 创建虚拟机。
- **不替代 Prometheus/Grafana**：只在作业结束时推送 Webhook 事件，不做时序指标大屏。
- **信任根**：本机磁盘清单（`servers.yaml`，权限 `0600`）。未知主机密钥默认拒绝；优先使用 SSH 密钥，若使用密码则会以明文保存在本地清单中，验证密钥可用后应执行 `ops server setup-key <name> --remove-password` 清除密码。

---

## 开源协议 (License)

基于 [Apache License 2.0](LICENSE) 协议开源。
