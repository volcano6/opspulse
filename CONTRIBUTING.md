# 贡献指南

感谢你对 OpsPulse 的关注与支持！

---

## 🛠️ 本地开发环境准备

### 前置依赖

- **Go 1.24+**
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

## 🚀 提交流程 (Pull Request)

1. Fork 本仓库
2. 创建新的功能分支 (`git checkout -b feat/my-new-feature`)
3. 提交代码修改
4. 运行 `make ci` 确保所有检查 100% 通过
5. 提交 Pull Request
