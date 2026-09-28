# 贡献指南 (Contributing)

OpsPulse 目前由单一维护者开发。Issue 与 PR 都欢迎，但请先开 issue 对齐方向，避免大改动白做。

## 1. 本地开发环境 (Local Setup)

- **Go 1.26+**（`go.mod` 的 `toolchain` 行固定了工具链补丁版本，`go` 命令会自动切换）
- **golangci-lint**
- **Docker**（可选，仅 e2e 与镜像构建需要）

```bash
make build   # 编译二进制
make test    # 运行单元测试与竞态检测
make lint    # 运行代码规范检查
make ci      # 本地全真 CI 流水线模拟（与 CI 逐 job 一致）
```

提交前跑一次 `make ci` 即可：`make ci` 通过 ⇔ GitHub Actions 通过。

## 2. 提交规范 (Commit Convention)

遵循 [Conventional Commits (约定式提交)](https://www.conventionalcommits.org/zh-hans/)：

```text
feat(ssh): 新增 --exec 入口
fix(1p): 空机器 restore 不再误报保留了本机独有服务器
docs(reference): 补充 server_ops 的 skip-batch 说明
ci: 升级 golangci-lint 版本
```

- `scope` 用变更所在模块（`ssh` / `1p` / `backup` / `restore` / `asset` / `template` / `docs` …），跨模块或纯构建改动可省略。
- 标题用中文短句说清"做了什么"；正文说明**为什么**改、**怎么验证**（命令或场景），不要只复述 diff。
- 不要加 AI 署名。

## 3. 中性化约定 (Neutrality)

本仓库是公开仓库，**所有已跟踪文件**（源码、测试夹具、文档、示例、CI 配置）都不得出现真实环境信息：

- **地址**：IPv4 只用 RFC 5737 文档段（`192.0.2.0/24`、`198.51.100.0/24`、`203.0.113.0/24`）、RFC 1918 私网段（`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`）或回环 `127.0.0.1`；IPv6 只用 RFC 3849（`2001:db8::/32`）与 `::1`。仓库里既有的「一眼假」占位（`1.1.1.x`、`1.2.3.x`、`2.2.2.2`、`3.3.3.3`、`5.6.7.8`、`9.9.9.9`）门禁同样放行，不必改写。
- **域名与邮箱**：只用 RFC 2606 的 `example.com` / `example.org` / `example.net`，或 `acme.*` 这类一眼可辨的占位；不得出现任何真实邮箱域名。
- **主机名与标签**：用 `web-01` / `db-01` / `oracle-sg` / `worker-1` 这类通用名，不要用真实机器别名。
- **路径**：用 `/home/user/...`、`C:\Users\user\...`、`/mnt/c/Users/user/...`；不要出现真实用户名或家目录。
- **凭据**：私钥只允许 `-----BEGIN ... PRIVATE KEY-----` 后跟 `...` 占位；`op://` 只允许默认库名（`Personal` / `Private` / `Work` / `Other` / `Employee` / `Shared` / `Team`）。
- **规模与指纹**：不要写真实机队数量、调用次数、内核版本号或 CPU 型号。
- **镜像与自建服务**：不要硬编码自建镜像源或私有仓库地址，只留公共兜底并通过参数显式传入。

`scripts/check-neutrality.sh` 按上述形状扫描所有已跟踪文件，`make ci` 会执行它。它**只做形状检查**——刻意不做通用 FQDN、base64 长串、裸数字匹配，那会误伤 Go import path、`go.sum` 与 semver。真正的词表（雇主名、真实公网 IP 等）**绝不进仓库**：词表本身一旦入库，就成了新的泄漏源，因此只保存在本地未跟踪的守卫里。

## 4. 文档 (Docs)

文档结构与维护约定见 [docs/README.md](docs/README.md)。`make docs-check` 校验命令参考与文档链接；`make docs-gen` 重新生成 `docs/reference/cli.md`（生成物，不要手改）。
