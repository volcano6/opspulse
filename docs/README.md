# OpsPulse 文档地图 (Documentation Map)

全部文档的唯一索引。文档只有三层：**教程**（照着做）、**参考**（查字段与参数）、**解释**（判断能不能信）。

## 1. 推荐阅读顺序 (Reading Order)

1. [新手入门教程](tutorial/getting_started.md)：装好、连上第一台机器、跑完初始化。
2. [配置与模板](reference/configuration.md) 与 [服务器与资产](reference/server_ops.md)：清单、配置文件与远端操作。
3. [备份、还原与调度指南](reference/backup.md)：备份、还原与定时调度（含告警触发时机）。
4. [架构与信任模型](explanation/architecture.md)：要判断「能不能信、边界在哪、它不做什么」时再读。

## 2. 按场景找文档 (By Task)

| 我要做什么 | 从这里开始 |
|---|---|
| 第一次上手：编译、加机器、初始化 | [新手入门教程](tutorial/getting_started.md) |
| 备份目录、数据库或容器 | [备份、还原与调度指南 § 备份](reference/backup.md#2-备份命令与实战-backup-commands) |
| 还原、跨机迁移、路径重映射 | [备份、还原与调度指南 § 还原](reference/backup.md#3-还原-restore)、[跨机迁移与环境备份教程](tutorial/migration.md) |
| 定时自动备份、失败时收到告警 | [备份、还原与调度指南 § 调度](reference/backup.md#4-调度与守护进程-scheduler)、[配置与模板 § 通知渠道](reference/configuration.md#4-通知渠道-notificationsyaml) |
| 管理服务器清单、资产、模板与目录 | [服务器与资产](reference/server_ops.md)、[配置与模板](reference/configuration.md) |
| 用 1Password 保存凭据、换机同步 | [1Password 备份与跨机同步指南](reference/onepassword.md)、[离线验证教程](tutorial/onepassword_offline_verify.md) |
| 查某个命令有哪些子命令和 flag | [命令行参考](reference/cli.md) |
| 理解信任边界、非目标与术语 | [架构与信任模型](explanation/architecture.md) |

## 3. 全部文档 (All Documents)

### 教程 (Tutorials)

| 文档 | 解决什么问题 |
|---|---|
| [新手入门教程](tutorial/getting_started.md) | 从源码编译、装 shell 补全、添加第一台服务器、跑 bootstrap。 |
| [跨机迁移与环境备份](tutorial/migration.md) | 容器跨机迁移（含改名、热导出与自动探活灌库），以及 WSL 环境备份与按路径重映射还原。 |
| [1Password 离线验证教程](tutorial/onepassword_offline_verify.md) | 没有 1Password 账号也能把 1P 备份/还原链路跑一遍并看懂调用序列。 |

### 参考 (Reference)

| 文档 | 解决什么问题 |
|---|---|
| [命令行参考](reference/cli.md) | 全部命令与 flag 的机械清单（由 `make docs-gen` 生成）。 |
| [配置与模板](reference/configuration.md) | 配置与数据目录、`servers.yaml` / `assets.yaml` 字段、`notifications.yaml` 渠道与 payload、模板 frontmatter、SQLite 落盘位置。 |
| [服务器与资产](reference/server_ops.md) | 服务器清单与标签检索、`server info`、`ssh` / `exec` / `cp` / `sftp`、远端容器 `ps` / `logs`、业务资产模型与引用保护。 |
| [备份、还原与调度指南](reference/backup.md) | `backups.yaml` 字段规范、备份命令、容器智能备份、还原参数与场景、Cron 调度与 systemd 部署。 |
| [1Password 备份与跨机同步指南](reference/onepassword.md) | `ops 1p` 的备份、还原、状态、配置与自检，以及已知边界与常见问题。 |

### 解释 (Explanation)

| 文档 | 解决什么问题 |
|---|---|
| [架构与信任模型](explanation/architecture.md) | 组件与数据流、执行模型、安全边界与信任模型、非目标与核心概念。 |

## 4. 故障排查 (Troubleshooting)

排障内容留在各自文档里，不另建页面：

- 命令补全不生效、`ops` 找不到：[新手入门教程 § 补全踩坑排查](tutorial/getting_started.md#补全踩坑排查-troubleshooting)
- 1Password 报错、凭据放在哪儿、WSL 下 `op` 连不上：[1Password 备份与跨机同步指南 § 已知边界](reference/onepassword.md#已知边界) 与 [§ 常见问题](reference/onepassword.md#常见问题)
- 离线验证脚本跑不过：[1Password 离线验证教程 § 边界与排障](tutorial/onepassword_offline_verify.md#3-边界与排障-limits-and-troubleshooting)

## 5. 文档之外 (Beyond These Docs)

[README.md](../README.md)（简介、安装、快速上手）、[SECURITY.md](../SECURITY.md)（漏洞上报与支持版本）、
[CONTRIBUTING.md](../CONTRIBUTING.md)（构建、提交与中性化约定）、[CHANGELOG.md](../CHANGELOG.md)（版本变更）。

## 6. 维护约定 (Maintenance)

- **一个事实一个家**：命令与 flag 的机械清单只在 `reference/cli.md`；配置目录与字段只在
  `reference/configuration.md`；备份、还原与调度的字段和参数只在 `reference/backup.md`；远端操作与业务资产
  只在 `reference/server_ops.md`；1Password 只在 `reference/onepassword.md`；非目标与信任模型只在
  `explanation/architecture.md`。别处重复出现的事实只留一句并链过去。
- **教程只放最小可跑序列**：参数表、字段规范与完整 flag 清单不进教程。
- **CLI 文案就是文档文案**：命令摘要、`Long` 说明与 flag 文案用中文写在 `cmd/opspulse/*.go`，`ops --help`
  与 `reference/cli.md` 是同一份文字的两个出口（用法与帮助模板在 `cmd/opspulse/main.go`）。改文案改 Go 源，
  再跑 `make docs-gen`；新增命令时按中文写，别留英文。
- **冻结清单**：不新增 CODE_OF_CONDUCT、CONTRIBUTORS、wiki、ADR 与文档专用 CHANGELOG；要加新页面前先问
  「它是不是已有页面的一节」。
- **版本变更不手写**：`CHANGELOG.md` 由 `make changelog`（`scripts/gen-changelog.sh`）从 conventional
  commit 生成，发版时写入。

---

`docs/reference/cli.md` 是生成物：内容来自 CLI 定义，由 `make docs-gen` 重写，`TestCLIReferenceUpToDate`
保证它与代码一致。**不要手工编辑**——改完命令或 flag 后跑一次 `make docs-gen` 即可。

`make docs-check` 是文档门禁（CI 同款）：生成物与命令树逐字节一致、文档里的相对链接与页内锚点可解析、
文档里出现的命令与 flag 都能在命令树里解析、命令树里每条命令都在手写文档里露过面。新增或改名命令后
如果忘了同步文档，这条门禁会直接失败。确有正当例外时，在该行加 `<!-- docs-check: ignore -->` 即可豁免该行
及其下一行。
