# OpsPulse 新手入门教程

本教程将引导你完成 OpsPulse 的编译安装、服务器清单配置以及从零初始化一台新服务器的完整流程。

---

## 1. 前置准备

- **Go 1.25+**（源码编译所需）
- **SSH 密钥对**（例如 `~/.ssh/id_ed25519` 或 `~/.ssh/id_rsa`）
- 至少一台具备 SSH 访问权限的 Linux 服务器（Ubuntu / Debian）。

---

## 2. 编译与安装

### 推荐方式：一键安装到用户目录

```bash
git clone https://github.com/volcano6/opspulse.git
cd opspulse
make install

# 验证编译产物
ops version
```

`make install` 会自动将可执行文件安装到 `$(go env GOPATH)/bin` 或 `~/.local/bin`。如果你的终端找不到命令，也可以手动放入系统 PATH：

```bash
sudo cp ./bin/ops /usr/local/bin/
ops version
```

---

## 3. 配置 Shell 自动补全（强烈推荐）

Ops 支持全自动 Shell 补全（Tab 键自动补全子命令、标志、服务器名称、模板名称及备份任务）。

### 全自动一键安装（推荐）

直接在终端执行（若当前终端尚未加载 PATH，可直接运行 `./bin/ops`）：

```bash
./bin/ops completion --install
source ~/.zshrc  # 若使用 Bash 则执行 source ~/.bashrc
```

该命令会自动探测你当前使用的 Shell（Bash / Zsh / Fish / PowerShell），并将对应的 PATH 保障逻辑与补全脚本安全、幂等地写入用户 Profile，同时若系统 PATH 中尚未发现 `ops`，会自动将其安装至 `~/.local/bin/ops` 与 `~/.local/bin/opspulse`。

### 常见踩坑排查（FAQ / Troubleshooting）

* ⚠️ **注意事项 1：按 Tab 键补全时不要带 `-h`**
  - **现象**：敲 `ops -h <Tab>` 时，终端列出了当前目录下的所有文件。
  - **原因**：`-h`（即 `--help`）是一个没有后续参数的布尔开关。在 `-h` 后面按下 Tab 时，补全引擎判断该选项已完结且无参数，Zsh / Bash 会自动触发兜底的文件名补全机制。
  - **正确操作**：
    - 想查看帮助：输入 `ops -h` 然后按 **回车 (Enter)**。
    - 想使用自动补全：输入 `ops <Tab>`（空格后直接按 Tab），即可列出所有子命令；输入 `ops l<Tab>` 会自动补全为 `ops ls`。
* ⚠️ **注意事项 2：提示 `找不到命令 “ops”` 或 `opspulse: unknown flag: --install`**
  - **原因**：
    1. `make install` 默认安装在 `~/go/bin` 与 `~/.local/bin`。若你的终端未将这些目录加入 `$PATH`，会提示找不到 `ops`；
    2. 若系统之前在 `/usr/local/bin` 残留了早期旧版本 `opspulse`，输入 `opspulse` 会误触发没有 `--install` 参数的旧程序。
  - **解决办法**：直接运行当前仓库构建出的 `./bin/ops completion --install`，然后执行 `source ~/.bashrc`（或 `source ~/.zshrc`），即可自动修复 PATH 并激活补全。若有旧残留，可用 `sudo rm -f $(which opspulse)` 清理。
* ⚠️ **注意事项 3：重新编译新版本后需同步二进制**
  - 新增子命令或更新后，运行 `make install` 即可同步覆盖最新二进制。
* ⚠️ **注意事项 4：当前打开的终端未生效**
  - 在当前打开的终端中执行一次 `rehash` 或 `source ~/.zshrc` 即可使新加入 PATH 的命令立即生效。

---

## 4. 第一步：添加并管理服务器 (Server Ops)

将你的 VPS 注册进 Ops 的清单库（支持设置自定义 labels 标签）：

```bash
# 极简添加 VPS（密码静默交互输入，自动引导一键注入公钥免密直连）
ops add web-01 198.51.100.10 \
  --labels provider=oracle,region=singapore,purpose=web \
  --tags prod,web \
  --desc "生产环境主 Web 节点"
```

### 极速查看所有服务器清单：
```bash
ops ls
# 支持按标签筛选
ops ls --filter provider=oracle
```

### 快速探查目标服务器硬件与系统状态：
```bash
ops server info web-01
```

### 免记密码/IP，一键建立原生交互式 SSH 终端连接：
```bash
# 无参数执行弹出交互式菜单，回车默认连第 1 台
ops ssh

# 或指定服务器名称秒连
ops ssh web-01
```

### 一键打通 VS Code / Cursor Remote-SSH 开发：
```bash
# 幂等写入 ~/.ssh/config，编辑器左侧即刻显示全部 VPS，点击免密直连
ops export ssh-config --write
```

### 测试与目标服务器的 SSH 连通性：
```bash
ops server test web-01
```

输出示例：
```text
Connecting to web-01 (198.51.100.10:22)...
✅ Connection successful!
   Latency : 12.45 ms
   System  : Linux 6.8.0-45-generic x86_64
```

---

## 5. 第二步：查看与发现可用模板

Ops 二进制中直接内置了常用的官方模板：

```bash
ops template list
```

输出示例：
```text
NAME       VER   TYPE       OS              DESCRIPTION
----       ---   ----       --              -----------
base       v1    built-in   ubuntu,debian   安装常用系统基础工具与依赖包
docker     v1    built-in   ubuntu,debian   安装官方 Docker CE 与 Docker Compose 插件
restic     v1    built-in   ubuntu,debian   安装 restic 与 rclone 备份工具链
security   v1    built-in   ubuntu,debian   基础安全加固（UFW 防火墙、fail2ban 防暴破）
```

查看某个具体模板的脚本源码与元数据：
```bash
ops template show docker
```

---

## 6. 第三步：执行服务器初始化 (Bootstrap)

### 安全模拟运行 (Dry Run)
在向远程服务器下发指令前，可以先通过 `--dry-run` 预览执行流程与脚本大小：

```bash
ops bootstrap web-01 -t base,security,docker --dry-run
```

### 正式执行初始化
确认无误后，去掉 `--dry-run` 开始正式执行：

```bash
ops bootstrap web-01 -t base,security,docker
```

Ops 将按以下流程工作：
1. 在终端实时输出带有 `[web-01]` 标签的前缀日志。
2. 自动在本地 `$XDG_DATA_HOME/opspulse/logs/bootstrap-web-01-<timestamp>.log` 记录全量日志。
3. 执行完成后输出结构化的结果汇总表格。
