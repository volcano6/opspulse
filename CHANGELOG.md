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
- `ops 1p doctor`：只读自检整条 1Password 链路——`op` 可执行文件的路径与构建选择（WSL 下必须是 Windows
  版）、可见账号、保险库与选择依据、本机备份条目的字节数/服务器数/私钥数、以及 `servers.yaml` 里残留的
  `op://` 引用和缺失的密钥文件。每步给 ok / warn / fail 与下一步命令；任一步 fail 即以非零退出，可直接
  当预检用；`--offline` 只跑不需要网络往返的检查，全程不写任何文件、不打印任何凭据。
- `ops server remove` / `ops asset remove` 删除前检查 `backups.yaml` 的引用：默认打印会因此失败的备份
  job 名；交互模式下 `server remove` 追加一次二次确认，`--yes` 与非交互脚本只告警不阻断。引用检查在
  `backups.yaml` 不可读时降级为「未检查」警告，绝不因此让删除失败。

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
- **行为变更：三个命令的并发语义统一为「默认 5」。** `ops exec` / `ops doctor` / `ops backup run` 的
  `-j/--parallel` 现在共用一条规则：不传或传 `0` 表示默认并发 5，`unlimited`（不区分大小写）表示不设上限，
  正整数表示该值，其它取值（含负数）直接报错退出。`ops backup run` 的默认值此前是 `0` 且表示**无限并发**，
  现在为 5；需要旧行为请显式写 `--parallel unlimited`（显式传 `-j 0` 时会打印一条迁移提示）。
  `-j 4` 等既有写法不受影响。
- **行为变更：容器备份的中间产物收进项目目录下的 `.opspulse/`。** 数据库热导、命名卷归档与 `manifest.yaml`
  此前直接写在容器的 Compose 工作目录根部（`dumps/`、`volumes/`、`manifest.yaml`），与用户自己的同名目录
  冲突——用相对挂载（如 `./volumes/db:/var/lib/postgresql/data`）的项目会被误删数据。现在统一放在
  `<项目目录>/.opspulse/{dumps,volumes,manifest.yaml}`，备份前的清理只碰这一个命名空间；旧快照仍可还原
  （还原时同时探测新位置与旧位置）。
- `ops exec` 批量输出分工收紧：各主机远程命令自己的输出仍进 stdout（按主机名加前缀），跳过提示、失败行与
  汇总统计改走 stderr。因此 `ops exec -f all "cat /etc/hosts" > hosts.txt` 得到的是干净结果。
- `ops 1p backup` 的 `op` 调用次数由稳定态 2 次变为 3 次（首次 4 → 5）：写入前先读回现有条目，用于继承本机
  已读不到的私钥，并在回读校验失败时回滚。多一次授权往返，换掉一整类「远端副本被静默清掉」。
- **门禁收敛为单一事实源**：`make ci` 成为唯一入口（`fmt-check` / `vet` / `vet-cross` / `test` / `cover` /
  `vuln` / `lint` / `build-cross` / `e2e` / `neutrality` / `docker`），`scripts/ci.sh` 与
  `.github/workflows/ci.yaml` 只调用这些目标，CI 的 9 个 job 全部并行。golangci-lint（v1.64.5）与
  govulncheck（v1.1.4）的版本只在 `Makefile` 出现一次、用 `go run <pkg>@<pin>` 调用，删除旧的
  revive/errcheck/ineffassign/gosec/staticcheck 五个 `@latest` 调用与 `make tools`——这正是「本地绿、CI 红」
  的漂移来源。新增覆盖率阈值（`COVER_MIN ?= 65`）与依赖漏洞门禁（`make vuln`，当前 0 可达漏洞）。
- **依赖与工具链底线提升到 Go 1.26**：`golang.org/x/crypto` v0.55.0 → v0.56.0（修复 GO-2026-6354 /
  GO-2026-6355，两者都要求 Go 1.26），因此 `go.mod` 的 `go` 行改为 `1.26.0`，并用 `toolchain go1.26.6`
  固定工具链补丁版本（govulncheck 报出的 14 项标准库告警全部由该补丁版本修复）。`Dockerfile` 同步到
  `golang:1.26-alpine`，并新增可覆盖的 `GOPROXY` 构建参数，使镜像也能在无法访问官方代理的网络中构建。
- 发布流程：`release.yaml` 新增 `make ci` 前置门禁（tag 不再绕过门禁），交叉编译加 `-trimpath`（发行包不再
  内嵌构建机绝对路径）；新增 `.dockerignore`，`make docker` 注入 VERSION/COMMIT/DATE 并断言镜像内的版本串。
- 新增 `.github/ISSUE_TEMPLATE/{bug_report,feature_request,config}.yml` 与 `PULL_REQUEST_TEMPLATE.md`
  （关闭空白 Issue）；`SECURITY.md` 补充私密上报渠道（GitHub Security Advisories）、支持版本范围，以及与
  既有安全原则对应的「凭据如何被处理」说明——含 `ops notify list` 打印完整 URL、`env:` 默认明文这两个真实例外。
- 文档与实现对齐（其中前两处此前**照抄即报错**）：`backups.yaml` 示例根键 `jobs:` → `backups:`；`ops exec`
  的 `--timeout` 位置；`paths` 必须是绝对路径且 `~` 不会被展开；容器备份的远端暂存目录 `<项目目录>/.opspulse/`；
  `ops exec` 的 stdout/stderr 分工；并发默认 5 与 `--parallel unlimited`；`ops 1p backup` 的 op 调用次数。
- `scripts/check-neutrality.sh`：去掉对 bash 4 `mapfile` 的依赖（macOS 自带 bash 3.2 上原先会静默跳过全部
  文件），文件列表为空时改为 `exit 2`，邮箱域规则改为大小写不敏感。

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
- **远端密钥副本被静默销毁**（1P）：本机某个私钥文件读不到（被 `rm`、权限不对）时，备份会生成一份不含该密钥的
  文档、整篇覆盖远端条目，随后拿同一份文档回读校验并打印 "verified byte for byte" 退出 0——远端唯一副本就这样
  没了。现在写入前先读回现有条目：读不到的密钥沿用上次备份的副本并在输出中说明；回读校验失败会把上一份文档原样
  写回；连旧条目都读不到时直接拒绝盲写。服务器已从 `servers.yaml` 移除时，其密钥被丢弃也会打印聚合告警。
- **明文密码确认门形同虚设**（1P）：无参数的 `ops 1p restore` 先把含明文密码的清单合并写进 `servers.yaml`，
  之后才统计「将写入几个明文密码」——此时计数必为 0，于是既不提问，也不在非交互环境下拒绝。现在确认门移到
  **任何写盘之前**；拒绝、或在非交互环境下不给 `--yes`，`servers.yaml` 逐字节不变。
- **瞬时失败会造出空仓库**：备份脚本此前把「任何 `restic snapshots` 失败」都当成「仓库还没建」，于是网络抖动、
  权限错误、后端不可达都会触发 `restic init`，在错误的位置创建仓库。现在只有 restic 明确报告仓库不存在时才
  init，其它失败原样上抛。
- **`--dry-run --asset` 必然失败**：`restic ls` 没有 `--include` 参数（正确的是 `--path`），一旦按资产做
  dry-run 预览就得到 unknown flag；`restore run --dry-run --asset <id>` 此前完全不可用。
- **还原可能拉起错误的容器**：manifest 探测在没有任何候选匹配当前 job 名时，会拿**最后一个**读到的 manifest
  去 `up -d`。现在只有 `app` 与 job 名（或 `--as` 别名）一致时才用 manifest 驱动还原，否则回落自启路径并打印
  候选清单的差异。
- **日志文件名可穿越目录**：日志名由 job 名/服务器名拼出，此前没有任何清洗。现在统一做路径段清洗（分隔符与
  控制字符替换、剥掉前导点、按 rune 截断），并在拼好后断言结果仍在 `logs` 目录内。
- **受管私钥被误删**：`~/.ssh/opspulse_archive/...` 这类把 `opspulse_` 当目录名用的路径、以及经该目录穿越出
  `~/.ssh` 的路径，此前都被当成 OpsPulse 托管的密钥删掉。现在判定改为「在 `~/.ssh` 之内**且**文件名以
  `opspulse_` 开头」，复用同文件里已有的 `filepath.Rel` 边界实现，不再用裸字符串前缀。
- **删除顺序倒置**：`ops server remove` 与 `ops server set --key` 此前先删私钥文件、后写清单，写盘失败就留下
  「条目指向已删密钥」的半损坏状态。现在先写清单、成功后再清理，清理失败降为警告（错误不再被静默丢弃）。
- **`ops cp` / `ops sftp` 不走跳板机**：两者此前完全不解析 `jump_host`，对只能经跳板机到达的主机会静默直连
  内网地址。现在 `ops cp` 与 `ops sftp --cli` 都会把跳板机（含其端口、用户与密钥）接进连接与 `ProxyCommand`，
  跳板机名不存在时直接报错；GUI SFTP 客户端无法隧道，遇到 `jump_host` 时明确拒绝并给出替代命令。
- **SFTP 上传可能删掉目标文件**：上传改为「先 rename 到备份名 → 落位 → 成功后再删备份」，落位失败时回滚；
  下载改为掩掉远端权限里的 group/other 写位（远端 0777 不再在本地变成 0777）。
- **卡住的 webhook 会吞掉下一次触发**：通知派发此前在 cron 任务体内同步执行，配合 `SkipIfStillRunning`，一个
  超时的 webhook 就能让该 job 的下一次触发被跳过。现在任务体只做非阻塞投递，由 2 个 worker 的有界队列异步派发
  （队列满则丢弃并告警），关闭时先停止触发、再等在途任务、最后带超时排空通知。
- **一个写坏的 cron 表达式拖垮整个 daemon**：`daemon` 此前遇到任意一个非法表达式就拒绝启动。现在逐 job 跳过
  非法项并打印警告，只有全部已调度 job 都非法时才报错退出。
- **`backups.yaml` 的坏输入会生成坏脚本**：`Job.Validate` 此前只校验名字非空。现在校验 job 名（拒绝路径分隔符、
  `..`、空白与控制字符、前导 `.`/`-`）、`paths` 必须为绝对路径、`env` 键名合法、`schedule` 形态合法，在加载或
  保存时就失败，而不是生成一个跑到目标机才炸的脚本。
- **容器备份的零碎正确性**：空 `HostPort` 不再生成 `127.0.0.1:` 这种畸形端口串；镜像名改为按仓库名精确匹配
  （`my-mysql-exporter` 不再被当成数据库容器去热导）；卷导出/导入脚本的文件名参数补上 shell 转义；还原侧的
  系统挂载判定改为复用 `docker.IsSystemMount`（`/development`、`/system` 这类前缀不再被误判）。

### 安全

- 已跟踪文件完成一次中性化：真实主机名、机队规模与本机指纹不再出现在源码、夹具与文档中。
- WSL 下为 Windows GUI 客户端镜像的私钥目录（`%USERPROFILE%\.ssh\opspulse\`）已在文档写明其位置、持久化的原因
  与手动清理方法；`ops sftp` 在密码认证下会把口令放进 GUI 客户端的命令行参数，文档已如实说明并建议改用密钥。
- 公开前对仓库历史与发行资产做了一次中性化处理。`v0.2.0` / `v0.3.0` 的发行包已于 2026-09-23 重建，
  内嵌 commit 指向重写后的提交，校验和与最初上传的文件不同，请以 release 页上的 `checksums.txt` 为准。
- **远端脚本的注入面**：`ops restore run --as <别名>` 与容器反译出的自启脚本此前用 Go 的 `%q` 当作 shell
  转义（二者不是一回事），构造出的别名可以在目标主机上执行任意命令。现在别名先过名字校验、所有插值改走
  `shellquote.Quote`，并补了「插值只作为字面量出现」的回归测试（真跑生成的脚本 + canary 文件断言）。
- **依赖漏洞门禁**：`make vuln` 跑 govulncheck，发现可达漏洞即失败（无忽略清单）。当前扫描结果为 0 个可达漏洞；
  `golang.org/x/crypto` 升级到 v0.56.0，修复 GO-2026-6354 / GO-2026-6355。

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
