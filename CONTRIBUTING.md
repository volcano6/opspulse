# 贡献指南

感谢你对 OpsPulse 的关注与支持！

---

## 🛠️ 本地开发环境准备

### 前置依赖

- **Go 1.25+**
- **golangci-lint**
- **Docker**（可选）

### 常用开发命令

```bash
# 编译二进制
make build

# 运行单元测试与竞态检测
make test

# 运行代码规范检查
make lint

# 运行本地全真 CI 流水线模拟
make ci
```

---

## 📝 Commit 提交规范

请遵循 [Conventional Commits (约定式提交)](https://www.conventionalcommits.org/zh-hans/)：

```text
feat(ssh): 新增 --exec 入口
fix(1p): 空机器 restore 不再误报保留了本机独有服务器
docs(reference): 补充 server_ops 的 skip-batch 说明
test(backup): 覆盖容器热导的失败路径
ci: 升级 golangci-lint 版本
```

- `scope` 用变更所在模块（`ssh` / `1p` / `backup` / `restore` / `asset` / `template` / `docs` …），跨模块或纯构建改动可省略。
- 标题用中文短句说清"做了什么"；正文说明**为什么**改、**怎么验证**（命令或场景），不要只复述 diff。

---

## 🧪 中性化约定（测试夹具、文档与示例）

本仓库是公开仓库，**所有已跟踪文件**（源码、测试夹具、文档、示例、CI 配置）都不得出现真实环境信息。
写测试和文档时请遵守：

- **地址**：IPv4 只用 RFC 5737 文档段（`192.0.2.0/24`、`198.51.100.0/24`、`203.0.113.0/24`）、
  RFC 1918 私网段（`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`）或回环 `127.0.0.1`；
  IPv6 只用 RFC 3849（`2001:db8::/32`）与 `::1`。仓库里既有的「一眼假」占位
  （`1.1.1.x`、`1.2.3.x`、`2.2.2.2`、`3.3.3.3`、`5.6.7.8`、`9.9.9.9`）门禁同样放行，不必改写。
- **域名与邮箱**：只用 RFC 2606 的 `example.com` / `example.org` / `example.net`，或 `acme.*`
  这类一眼可辨的占位；不得出现任何真实邮箱域名。
- **主机名与标签**：用 `web-01` / `db-01` / `oracle-sg` / `worker-1` 这类通用名，不要用真实机器别名。
- **路径**：用 `/home/user/...`、`C:\Users\user\...`、`/mnt/c/Users/user/...`；
  不要出现真实用户名或家目录。
- **凭据**：私钥只允许 `-----BEGIN ... PRIVATE KEY-----` 后跟 `...` 占位；`op://` 只允许默认库名
  （`Personal` / `Private` / `Work` / `Other` / `Employee` / `Shared` / `Team`）。
- **规模与指纹**：不要写真实机队数量、调用次数、内核版本号或 CPU 型号。
- **镜像与自建服务**：不要硬编码自建镜像源或私有仓库地址，只留公共兜底并通过参数显式传入。

`scripts/check-neutrality.sh` 会按上述形状扫描所有已跟踪文件，CI 与 `make ci` 都会执行它。
它**只做形状检查**——刻意不做通用 FQDN、base64 长串、裸数字匹配，那会误伤 Go import path、
`go.sum` 与 semver。真正的词表（雇主名、真实公网 IP 等）**绝不进仓库**，只存在于本地守卫
`.trellis/tools/guard.sh`：词表本身一旦入库，就成了新的泄漏源。

---

## 🚀 提交流程 (Pull Request)

1. Fork 本仓库
2. 创建新的功能分支 (`git checkout -b feat/my-new-feature`)
3. 提交代码修改
4. 运行 `make ci` 确保所有检查 100% 通过
5. 提交 Pull Request
