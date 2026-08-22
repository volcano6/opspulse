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

请遵循 [Conventional Commits (约定式提交)](https://www.conventionalcommits.org/zh-hans/):

```text
feat: add server inventory
fix: handle SSH timeout
docs: update README
test: add executor tests
ci: update GitHub Actions
```

---

## 🚀 提交流程 (Pull Request)

1. Fork 本仓库
2. 创建新的功能分支 (`git checkout -b feat/my-new-feature`)
3. 提交代码修改
4. 运行 `make ci` 确保所有检查 100% 通过
5. 提交 Pull Request
