# OpsPulse 新手入门教程 (Getting Started)

这篇教程带你从零跑通一条完整链路：编译安装 → 添加第一台服务器 → 用内置模板完成一次初始化。
每一步只给「照着做就能跑通」的最小序列，完整参数、字段与 flag 都指向对应的参考页。

## 1. 前置准备 (Prerequisites)

- **Go 1.26+**（源码编译所需）
- **SSH 密钥对**（例如 `~/.ssh/id_ed25519` 或 `~/.ssh/id_rsa`）
- 至少一台具备 SSH 访问权限的 Linux 服务器（Ubuntu / Debian）

## 2. 编译与安装 (Build and Install)

```bash
git clone https://github.com/volcano6/opspulse.git
cd opspulse
make install

# 验证编译产物
ops version
```

`make install` 会把可执行文件装到 `$(go env GOPATH)/bin` 与 `~/.local/bin`（`ops` 与
`opspulse` 是同一个二进制的两个名字）。若终端找不到命令，也可以手动放进系统 PATH：

```bash
sudo cp ./bin/ops /usr/local/bin/
ops version
```

## 3. 配置 Shell 自动补全 (Shell Completion)

`ops` 支持 Tab 补全子命令、flag、服务器名、模板名与备份任务名。一条命令完成探测与写入：

```bash
./bin/ops completion --install
source ~/.zshrc  # 若使用 Bash 则执行 source ~/.bashrc
```

该命令会自动探测当前 Shell（Bash / Zsh / Fish / PowerShell），把对应的 PATH 保障逻辑与补全
脚本幂等写入用户 profile；若系统 PATH 中还没有 `ops`，会同时装到 `~/.local/bin/ops` 与
`~/.local/bin/opspulse`。

### 补全踩坑排查 (Troubleshooting)

- **Tab 补全时不要带 `-h`**：`-h`（即 `--help`）是没有后续参数的布尔开关，在它后面按 Tab 会
  触发 Zsh / Bash 的文件名兜底补全，于是列出当前目录下的所有文件。看帮助请敲 `ops -h` 后回车；
  要用补全请敲 `ops <Tab>`（空格后直接按 Tab），输入 `ops l<Tab>` 会自动补成 `ops ls`。
- **提示找不到 `ops`，或 `opspulse: unknown flag: --install`**：前者是 `~/go/bin` 与
  `~/.local/bin` 不在 `$PATH`；后者是系统里残留了早期旧版本的 `/usr/local/bin/opspulse`。
  直接运行当前仓库构建出的 `./bin/ops completion --install`，再 `source ~/.bashrc`（或
  `~/.zshrc`）即可修复 PATH 并激活补全；旧残留可用 `sudo rm -f $(which opspulse)` 清理。
- **重新编译新版本后没生效**：再跑一次 `make install` 覆盖最新二进制。
- **当前打开的终端没生效**：执行一次 `rehash` 或 `source ~/.zshrc`。

## 4. 添加第一台服务器 (Add Your First Server)

```bash
# 极简添加 VPS（密码静默交互输入，连通后引导一键注入公钥实现免密直连）
ops add web-01 198.51.100.10 \
  --labels provider=oracle,region=singapore,purpose=web \
  --tags prod,web \
  --desc "生产环境主 Web 节点"

# 查看清单（支持按 label / tag 过滤）
ops ls
ops ls --filter provider=oracle

# 测试 SSH 连通性与网络延迟
ops server test web-01
```

输出示例：

```text
Connecting to web-01 (198.51.100.10:22)...
✅ Connection successful!
   Latency : 12.45 ms
   System  : Linux 6.8.0-45-generic x86_64
```

`ops add` 的全部 flag、跳板机、批量跳过保护等写法见[服务器与资产](../reference/server_ops.md)；
交互式登录（`ops ssh`）、远程单命令（`ops exec`）、文件传输（`ops cp` / `ops sftp`）与
VS Code / Cursor 联动（`ops export ssh-config --write`）也在同一篇里。

## 5. 查看可用模板 (Browse Templates)

```bash
ops template list          # 列出全部内置模板
ops template show docker   # 查看某个模板的元数据与脚本源码
```

内置模板清单、frontmatter 字段与自定义模板目录见[配置与模板 § 模板与 Frontmatter](../reference/configuration.md#6-模板与-frontmatter-templates)。

## 6. 初始化服务器 (Bootstrap)

先预览执行计划，确认后再正式执行：

```bash
ops bootstrap web-01 -t base,security,docker --dry-run
ops bootstrap web-01 -t base,security,docker
```

执行期间：终端按 `[web-01]` 前缀实时输出，全量日志落在
`$XDG_DATA_HOME/opspulse/logs/bootstrap-web-01-<timestamp>.log`，结束后打印结构化结果汇总表。
模板传参（如 `-t nodejs:22`）与多机批量执行的细节见[配置与模板 § 模板与 Frontmatter](../reference/configuration.md#6-模板与-frontmatter-templates)。

## 7. 下一步 (Next Steps)

- [备份、还原与调度指南](../reference/backup.md)：声明式备份作业、容器一键备份与定时调度
- [跨机迁移与环境备份教程](migration.md)：容器跨机迁移、WSL 环境备份与路径重映射
- [配置与模板](../reference/configuration.md)：清单、数据库与日志都落在哪
- [命令行参考](../reference/cli.md)：全部命令与每个 flag 的默认值
