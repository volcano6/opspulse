# 日常服务器管理指南 (Server Operations)

Ops 不仅是一键初始化与灾备迁移平台，更是日常高效管理多台 VPS 的核心入口。

---

## 1. 服务器列表与标签检索

### 极简添加服务器 (`ops add`)

支持极简位置参数语法：`ops add <name> [user@]host[:port] [flags]`。省略 `user` 默认为 `root`，省略 `port` 默认为 `22`。

```bash
# 1. 极简添加（密码安全交互输入，连通后可交互式一键注入公钥免密直连）
ops add vps-1 168.138.1.1

# 2. 指定自定义用户、端口与私钥
ops add oracle-sg ubuntu@168.138.1.1:2222 \
  -i ~/Downloads/oracle.pem \
  --labels provider=oracle,region=singapore,purpose=blog \
  --tags prod,web \
  --desc "生产环境博客主节点"

# 3. 通过跳板机（Bastion / Jump Host）添加内网机器（免查内网 IP，名字由跳板机解析）
ops add vps2 ubuntu@vps2 -J vps1 -p 123456

# 4. 亦可使用传统全 flag 形式（兼容 ops server add）
ops server add oracle-sg --host 168.138.1.1 --user ubuntu -i ~/.ssh/id_ed25519
```

> 🛡️ **密码静默输入与公钥免密直连引导**：
> - **跳板机穿透（Jump Host）**：通过 `-J / --jump-host <server_name>` 关联跳板机，支持连续按 `<Tab>` 补全现有服务器。内网机器的主机名（如 `vps2`）将直接由跳板机在远程内网中解析，无需事先查探内网 IP。所有 SSH、SFTP、命令执行、自动化备份与 VS Code Remote-SSH（自动导出 `ProxyJump`）均走端到端加密通道，私钥无需在跳板机上落地。
> - **静默密码交互**：未提供 `-i` 私钥时，终端会提示输入 SSH 密码，输入过程静默隐藏不回显，绝不留在 Shell 历史记录中。
> - **公钥一键注入**：首次密码连通成功后，Ops 会检测本地通用公钥（如 `~/.ssh/id_ed25519.pub` 或 `~/.ssh/id_rsa.pub`，若无则自动生成专用密钥），并询问是否注入远端 VPS 的 `~/.ssh/authorized_keys`。注入成功并验证通过后，本地自动升级为密钥直连认证，且清空本地密码明文存储，兼顾极速与安全。
> - **私钥安全位置迁移**：当 `-i <path>` 指向 `~/.ssh/` 之外的目录（例如 `~/Downloads/` 或临时目录）时，Ops 会交互式询问是否将密钥复制到 `~/.ssh/opspulse_<server_name>.pem` 并自动设置为 `0600` 权限（可用 `--no-copy-key` 跳过复制）。
> - **即时连通性验证与失败回滚**：`ops add` 默认自动测试 SSH 连通性。若认证失败，会自动清理复制的临时私钥且不保存错误服务器（离线服务器可通过 `--skip-test` 跳过验证）。
> - **生命周期与依赖保护**：通过 `ops server remove <name>` 删除服务器时，若存在下游内网机器正以此机器作为跳板机，OpsPulse 会自动拦截并提示依赖关系，防止误删导致内网机失联。

### 增量更新与交互编辑

```bash
# 仅修改指定字段，其他配置保持不变；--key "" 可清除绑定密钥
ops server set oracle-sg --port 2222
ops server set oracle-sg --host 203.0.113.10 --key ~/.ssh/oracle-sg

# 使用 $VISUAL、$EDITOR 或系统默认编辑器打开完整清单并定位该服务器
ops server edit oracle-sg
```

`server edit` 在临时副本中编辑。编辑器正常退出后才校验 YAML、服务器字段和名称唯一性；校验失败或目标服务器被删除时，原 `servers.yaml` 保持不变。

### 多维筛选过滤 (`ops ls` / `--filter`)

支持使用极简顶级命令 `ops ls`（等价于 `ops server list`），并支持灵活的多维筛选过滤：

```bash
# 1. 快速列出所有配置的服务器
ops ls

# 2. 按 Label 键值对精确过滤
ops ls --filter provider=oracle

# 3. 按 Label 键名或取值模糊过滤
ops ls --filter singapore

# 4. 按 Tag 分组过滤
ops ls --filter prod

# 5. 按服务器名称匹配
ops ls --filter oracle-sg
```

输出示例：
```text
NAME          HOST          PORT   USER     AUTH                    LABELS                                                TAGS       DESCRIPTION
----          ----          ----   ----     ----                    ------                                                ----       -----------
oracle-sg     168.138.1.1   22     ubuntu   key (~/.ssh/id_ed25519) purpose=blog,provider=oracle,region=singapore         prod,web   生产环境博客主节点
```

---

## 2. 系统与硬件资源探测 (`server info`)

无需在远程服务器安装任何 Agent，Ops 通过原生单次 SSH 会话聚合采集系统的关键硬件资源与运行时指标：

```bash
ops server info oracle-sg
```

输出展示：
```text
╔═══════════════════════════════════════════════════════════════╗
║  Server : oracle-sg                                           ║
║  Host   : 168.138.1.1:22                                      ║
╠═══════════════════════════════════════════════════════════════╣
   OS           : Ubuntu 24.04 LTS
   Kernel       : 6.8.0-45-generic
   CPU          : 2 Cores (Ampere Altra)
   Memory       : 12.00 GB (2.10 GB used)
   Disk         : 80.00 GB (23.00 GB used / 57.00 GB free)
   Swap         : 2.00 GB (0 B used)
   Uptime       : up 42 days, 5 hours
   ------------------------------------------------------------
   Docker       : 27.5.1 ✓
   Containers   : 5 running / 2 stopped
   BBR          : bbr ✓
╚═══════════════════════════════════════════════════════════════╝
```

---

## 3. 交互式原生 SSH 直连 (`ssh`) 与 VS Code 联动 (`export ssh-config`)

无需记忆服务器地址、端口、密码或私钥，Ops 提供极简的终端直连与开发工具链打通能力：

### 终端极速交互秒连 (`ops ssh`)

```bash
# 1. 无参数直连：弹出清晰选择菜单，回车默认连第 1 台，或输入序号/名称直连（单机时直接免选直连）
ops ssh

# 2. 指定名称直达会话；配置 password 时自动认证，无需二次输入
ops ssh oracle-sg

# 3. 将密码认证一键转换为专用密钥认证
ops server setup-key oracle-sg

# 4. 透传原生 SSH 客户端选项（使用 -- 分隔）
ops ssh oracle-sg -- -o StrictHostKeyChecking=no

# 5. 远程快速启动特定命令或 tmux
ops ssh oracle-sg -- tmux attach
```

### 一键打通 VS Code / Cursor / 系统终端 (`ops export ssh-config`)

将 Ops 清单中的所有服务器一键渲染并幂等写入系统 `~/.ssh/config`，自动创建 Ops 受控区块，**绝不影响用户原有的其他 Host 配置**：

```bash
# 1. 打印生成的 OpenSSH 配置预览
ops export ssh-config

# 2. 幂等写入 ~/.ssh/config
ops export ssh-config --write

# 3. 筛选指定标签或环境的服务器导出
ops export ssh-config --write --filter env=prod
```

执行 `--write` 后：
- **VS Code / Cursor Remote-SSH**：左侧“远程资源管理器”自动感知所有 VPS，点击即免密秒连开发！
- **原生系统终端**：直接输入 `ssh oracle-sg` 原生秒连，无需再手动配置 `~/.ssh/config`。

绑定 `key_path` 后，原生 SSH 会自动追加 `IdentitiesOnly=yes`，只提交该私钥，避免 ssh-agent 中多把密钥触发 `Too many authentication failures`。`server add --key` 支持补全 `id_*` 和 `*.pem` 私钥文件。

非交互 SSH 执行与 SFTP 使用 TOFU 主机密钥策略：首次连接将主机密钥写入 `~/.ssh/known_hosts`，后续密钥不匹配时拒绝连接。首次连接前仍应通过可信渠道核对服务器指纹。`servers.yaml` 中的 `password` 是权限为 `0600` 的明文字段，请优先执行 `server setup-key` 后从配置中移除密码。

> **设计优势**：
> - **密钥模式（Linux / macOS）**：采用系统底层进程替换（`syscall.Exec`），保证原生 PTY 交互体验。
> - **密码模式及 Windows**：桥接标准终端，并通过受限临时文件向 OpenSSH `SSH_ASKPASS` 传递密码；密码不出现在命令参数或环境变量值中。

---

## 4. 远程单命令快速执行 (`exec`)

无需登录交互终端，直接在本地对指定远程主机执行单条命令，实时流式返回标准输出/标准错误，并完整保留远程命令退出码（支持免引号参数）：

```bash
# 1. 快速查看 Docker 容器列表
ops exec oracle-sg docker ps

# 2. 查看磁盘或内存情况（可直接在本地通过管道符处理）
ops exec oracle-sg df -h /
ops exec oracle-sg cat /var/log/nginx/access.log | grep 404 | wc -l

# 3. 设置超时时间（默认 60 秒，传 0 禁用超时）
ops exec oracle-sg "apt-get update" --timeout 120s
```

---

## 5. SFTP 统一双向文件传输 (`cp`)

基于高性能 SFTP 子系统，直接在本地与远程服务器之间进行统一双向文件或目录传输（支持自动识别远端前缀、递归拷贝与断点覆盖）：

### 本地上载到远端

```bash
# 1. 单个文件上传
ops cp ./nginx.conf oracle-sg:/etc/nginx/nginx.conf

# 2. 递归目录上传（必须加 -r / --recursive 参数）
ops cp -r ./configs/ oracle-sg:/opt/app/configs/
```

### 远端下载到本地

```bash
# 1. 单个文件下载到本地
ops cp oracle-sg:/var/log/nginx/error.log ./error.log

# 2. 递归目录下载到本地
ops cp -r oracle-sg:/var/data/ghost/ ./ghost-backup/
```

---

## 6. 外部 GUI SFTP 客户端快捷唤起 (`sftp`)

`ops sftp` 自动检测并唤起本机已安装的图形化 SFTP 客户端，自动装配主机的网络地址、端口、用户与私钥/密码凭证。

GUI 客户端以异步独立进程拉起，终端立即返回可用。

### 支持的客户端列表

| 操作系统 | 支持的 GUI 客户端 | 默认检测优先级 |
|---------|------------------|----------------|
| **Windows** | WinSCP、NetSarang Xftp (7/8)、FileZilla | PATH -> WinSCP -> Xftp -> FileZilla |
| **macOS** | Cyberduck、Panic Transmit、FileZilla | Cyberduck -> FileZilla -> Transmit |
| **Linux** | FileZilla、Nautilus (GNOME Files)、Dolphin、xdg-open | FileZilla -> Nautilus -> xdg-open |

### 常见用法

```bash
# 1. 查看本机检测到的所有可用 SFTP 客户端
ops sftp --list-apps

# 2. 自动唤起默认客户端连接指定服务器
ops sftp oracle-sg

# 3. 无参执行：弹出交互式菜单秒选服务器
ops sftp

# 4. 指定特定客户端（如 xftp 或 winscp 或绝对路径）
ops sftp oracle-sg --app xftp
ops sftp oracle-sg --app winscp
ops sftp oracle-sg --app "C:\custom\path\client.exe"

# 5. 指定打开的远端初始路径（默认为 /）
ops sftp oracle-sg --path /var/log/nginx

# 6. 使用终端原生 OpenSSH sftp 会话（非 GUI 模式）
ops sftp oracle-sg --cli
```

