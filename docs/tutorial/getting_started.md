# OpsPulse 新手入门教程

本教程将引导你完成 OpsPulse 的编译安装、服务器清单配置以及从零初始化一台新服务器的完整流程。

---

## 1. 前置准备

- **Go 1.24+**（源码编译所需）
- **SSH 密钥对**（例如 `~/.ssh/id_ed25519` 或 `~/.ssh/id_rsa`）
- 至少一台具备 SSH 访问权限的 Linux 服务器（Ubuntu / Debian）。

---

## 2. 编译与安装

### 方式 A：源码编译

```bash
git clone https://github.com/volcano6/opspulse.git
cd opspulse
make build

# 验证编译产物
./bin/opspulse version
```

### 方式 B：加入系统 PATH

你可以将编译出的二进制文件移动到系统的 PATH 路径中，方便全局直接调用：

```bash
sudo cp ./bin/opspulse /usr/local/bin/
opspulse version
```

---

## 3. 配置 Shell 自动补全（强烈推荐）

OpsPulse 支持全自动 Shell 补全（Tab 键自动补全子命令、标志、服务器名称、模板名称及备份任务）。

### 各终端一键配置命令

* **Bash（Linux / WSL / macOS）**：
  ```bash
  # 1. 确保安装了 Linux 补全库（Debian/Ubuntu 环境）
  sudo apt-get install -y bash-completion

  # 2. 写入系统级自动补全目录（全局生效）
  opspulse completion bash | sudo tee /etc/bash_completion.d/opspulse > /dev/null
  source /etc/bash_completion.d/opspulse
  ```
  *或者仅在当前用户生效（写入 `~/.bashrc`）：*
  ```bash
  echo 'source <(opspulse completion bash)' >> ~/.bashrc
  source ~/.bashrc
  ```

* **Zsh（Oh-My-Zsh / Starship 用户）**：
  ```bash
  # 写入 ~/.zshrc（永久生效）
  echo 'source <(opspulse completion zsh 2>/dev/null)' >> ~/.zshrc
  source ~/.zshrc
  ```

* **Fish**：
  ```bash
  opspulse completion fish > ~/.config/fish/completions/opspulse.fish
  ```

* **PowerShell（Windows）**：
  ```powershell
  # 当前会话生效
  opspulse completion powershell | Out-String | Invoke-Expression

  # 永久生效（写入 PowerShell Profile）
  if (!(Test-Path -Path $PROFILE)) { New-Item -ItemType File -Path $PROFILE -Force }
  opspulse completion powershell >> $PROFILE
  ```

### 常见踩坑排查（FAQ / Troubleshooting）

* ⚠️ **注意事项 1：必须使用系统全局命令名，不能带相对路径**
  - **现象**：敲 `./bin/opspulse <Tab>` 无法触发补全，但敲 `opspulse <Tab>` 正常。
  - **原因**：Cobra 补全规则默认绑定的是注册在系统的 `opspulse` 命令。若使用相对路径 `./bin/opspulse`，Shell 无法识别触发。建议通过 `sudo cp bin/opspulse /usr/local/bin/` 或将 `bin/` 目录加入 `$PATH`。
* ⚠️ **注意事项 2：重新编译新版本后需同步二进制**
  - 新增子命令或更新后，如果未将最新二进制覆盖到 `/usr/local/bin/opspulse`，补全列表依然会显示旧版命令集。
* ⚠️ **注意事项 3：环境降级为“文件列表”的排查**
  - 如果按 Tab 键只列出当前目录的文件，说明当前终端未加载 `bash-completion` 主引擎，需先执行 `source /usr/share/bash-completion/bash_completion`。

---

## 4. 第一步：添加纳管服务器

将你的 VPS 注册进 OpsPulse 的清单库：

```bash
# 使用默认 SSH 私钥添加一台 VPS
opspulse server add web-01 \
  --host 198.51.100.10 \
  --user root \
  --port 22 \
  --tags prod,web \
  --desc "生产环境主 Web 节点"
```

测试与目标服务器的 SSH 连通性：
```bash
opspulse server test web-01
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

OpsPulse 二进制中直接内置了常用的官方模板：

```bash
opspulse template list
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
opspulse template show docker
```

---

## 6. 第三步：执行服务器初始化 (Bootstrap)

### 安全模拟运行 (Dry Run)
在向远程服务器下发指令前，可以先通过 `--dry-run` 预览执行流程与脚本大小：

```bash
opspulse bootstrap web-01 -t base,security,docker --dry-run
```

### 正式执行初始化
确认无误后，去掉 `--dry-run` 开始正式执行：

```bash
opspulse bootstrap web-01 -t base,security,docker
```

OpsPulse 将按以下流程工作：
1. 在终端实时输出带有 `[web-01]` 标签的前缀日志。
2. 自动在本地 `$XDG_DATA_HOME/opspulse/logs/bootstrap-web-01-<timestamp>.log` 记录全量日志。
3. 执行完成后输出结构化的结果汇总表格。
