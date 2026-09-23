# 日常服务器管理指南 (Server Operations)

Ops 不仅是一键初始化与灾备迁移平台，更是日常高效管理多台 VPS 的核心入口。

---

## 1. 服务器列表与标签检索

### 极简添加服务器 (`ops add`)

支持极简位置参数语法：`ops add <name> [user@]host[:port] [flags]`。省略 `user` 默认为 `root`，省略 `port` 默认为 `22`。

```bash
# 1. 极简添加（密码安全交互输入，连通后可交互式一键注入公钥免密直连）
ops add vps-1 203.0.113.10

# 2. 指定自定义用户、端口与私钥
ops add oracle-sg ubuntu@203.0.113.10:2222 \
  -i ~/Downloads/oracle.pem \
  --labels provider=oracle,region=singapore,purpose=blog \
  --tags prod,web \
  --desc "生产环境博客主节点"

# 3. 通过跳板机（Bastion / Jump Host）添加内网机器（免查内网 IP，名字由跳板机解析）
ops add vps2 ubuntu@vps2 -J vps1 -p 2222

# 4. 敏感/公司服务器防手滑保护（跳过 ops exec -f all 与 ops doctor 等隐式批量运维）
ops add company-srv 10.0.0.1 --skip-batch

# 5. 亦可使用传统全 flag 形式（兼容 ops server add）
ops server add oracle-sg --host 203.0.113.10 --user ubuntu -i ~/.ssh/id_ed25519
```

> 🛡️ **密码静默输入与公钥免密直连引导**：
> - **服务器名规范（下划线改中划线）**：新增时服务器名中的下划线会统一改写为中划线，`ops add web_1` 实际保存为 `web-1`，避免 `web_1` 与 `web-1` 这类肉眼难分的名字让同一台机器在清单里出现两次（会打印一行提示）。已存在的条目不会被改名；若改写后的名字已被占用，`ops add` 直接报错而不是覆盖原条目。若清单里已有下划线名字（例如从备份恢复而来），再次添加它的中划线形式时会提示二者只差下划线。
> - **隐式批量防呆保护（SkipBatch）**：对于公司服务器、归档节点或特殊机器，配置 `--skip-batch`。在执行 `ops exec -f all` 或 `ops doctor` 等过滤式批量操作时会自动跳过，防止手滑误伤。显式单机操作（如 `ops ssh <name>`、`ops exec <name>`）不受任何影响；如确需批量包含，可临时传递 `--include-skipped` 覆盖。
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

# 随时开启或撤销批量跳过保护
ops server set company-srv --skip-batch
ops server set company-srv --no-skip-batch

# 使用 $VISUAL、$EDITOR 或系统默认编辑器打开完整清单并定位该服务器
ops server edit oracle-sg
```

`server edit` 在临时副本中编辑。编辑器正常退出后才校验 YAML、服务器字段和名称唯一性；校验失败或目标服务器被删除时，原 `servers.yaml` 保持不变。

### 删除服务器确认 (`ops server remove`)

`ops server remove <name>`（别名 `rm` / `delete`）删除清单条目的同时，还会删除 OpsPulse 为该机托管的私钥文件，因此默认要求交互确认：

```bash
# 1. 交互式删除：提示默认答案为 No，直接回车即取消
ops server remove old-vps

# 2. 非交互脚本中必须显式接受
ops server remove old-vps --yes

# 3. 只删清单条目，保留托管私钥
ops server remove old-vps --keep-key
```

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `-y, --yes` | 布尔 | `false` | 跳过删除确认提示；非交互 shell（stdin 非终端）下必须显式提供，否则直接拒绝执行 |
| `--keep-key` | 布尔 | `false` | 不删除磁盘上由 OpsPulse 托管的私钥文件（若有其他服务器仍引用同一密钥，本来也不会删除） |

交互提示的默认答案是 No：回车或回答 `n` 会中止操作并报 `server removal cancelled by user`，清单与私钥都保持原样。在非交互 shell（CI、脚本、管道）中不带 `--yes` 则直接拒绝，不会挂起等待输入：

```text
Error: refusing to remove server "old-vps" without confirmation; re-run with --yes to accept this in a non-interactive shell
```

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
oracle-sg     203.0.113.10  22     ubuntu   key (~/.ssh/id_ed25519) purpose=blog,provider=oracle,region=singapore         prod,web   生产环境博客主节点
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
║  Host   : 203.0.113.10:22                                     ║
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

# 3b. 同上，并在验证新密钥可用后清除 servers.yaml 中的明文密码
ops server setup-key oracle-sg --remove-password

# 4. 透传原生 SSH 客户端选项（使用 -- 分隔）
ops ssh oracle-sg -- -o StrictHostKeyChecking=no
ops ssh oracle-sg -- -v          # 调试握手过程
ops ssh oracle-sg -- -T          # 强制不分配 pty（纯管道场景）

# 5. 远程执行单条命令（非交互路径）
ops ssh oracle-sg --exec "tmux attach"
ops ssh oracle-sg --exec "systemctl status nginx | tail -n 20"
```

> **`--` 与 `--exec` 的分工**：`--` 之后的参数原样进入 ssh(1) 的**选项槽位**（`-o` / `-L` / `-v` / `-T` …）。该槽位必须位于目的地址之前，也是 ssh(1) 唯一接受选项的位置，因此远程命令无法经 `--` 传递——ssh(1) 会把命令词当成主机名。ops 在建立连接前就会拒绝这类参数，并提示改用 `--exec`。

`--exec` 复用同一个系统 ssh(1) 进程，但按非交互语义运行：ops 不改写终端标题、不过滤输出，横幅信息写 stderr，因此 **stdout 只承载命令输出**（可直接进管道）；**远程退出码原样成为 ops 的退出码**；pty 沿用 ssh(1) 的原生规则——stdin 是终端时分配（`tmux attach`、交互式 `sudo` 因此仍然可用），管道与脚本中得到纯非交互会话，需要强制关闭时追加 `-- -T`。需要超时控制、自动提权、批量并发或 Go 侧主机密钥策略时改用 [`ops exec`](#4-远程单命令快速执行-exec)；两条路径的逐项差异见 §4 末尾的对照表。

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

非交互 SSH 执行与 SFTP 默认执行**严格主机密钥校验**：主机密钥不在 `~/.ssh/known_hosts` 中时**直接拒绝连接**，并打印密钥类型与 SHA256 指纹；只有显式设置 `OPSPULSE_TRUST_NEW_HOST_KEY=1`（或 `true`）才会在首次连接时受信并写入 `~/.ssh/known_hosts`，之后应及时取消该变量以恢复严格校验。已记录的主机密钥不匹配时同样拒绝连接（需 `ssh-keygen -R <host>` 清除旧行后再连）。首次连接前仍应通过可信渠道核对服务器指纹。`servers.yaml` 中的 `password` 是权限为 `0600` 的明文字段，请优先执行 `server setup-key --remove-password`（安装密钥并在验证可用后自动清除明文密码）。

> **设计优势**：
> - **密钥模式（Linux / macOS）**：采用系统底层进程替换（`syscall.Exec`），保证原生 PTY 交互体验。
> - **密码模式及 Windows**：桥接标准终端，并通过受限临时文件（0700 临时目录 + 0600 文件，连接退出后自动写零并删除）向 OpenSSH `SSH_ASKPASS` 传递密码；密码不出现在命令参数、环境变量或进程列表中。凭据必须是本地明文——`servers.yaml` 中残留的 `op://` 引用会在连接前被拒绝，并提示运行 `ops 1p restore` 迁移。
> - **老旧主机兼容（`legacy-ssh`）**：针对仅提供 `ssh-rsa` / `ssh-dss` 的老旧主机，在 `servers.yaml` 中为其添加 `legacy-ssh` / `legacy_ssh` / `legacy-rsa` 标签或 `legacy-ssh: "true"` label 即可受控开启算法向下兼容（现代主机不受影响）。对于 `ops exec` / `ops cp` 等基于 Go `x/crypto/ssh` 的底层非交互调用，已默认支持 `ssh-rsa` 主机密钥协商；若主机仅提供 `ssh-dss`（DSA 算法已被 Go 官方库废弃），建议使用系统 OpenSSH 交互命令 `ops ssh` 登录维护。

---

## 4. 远程单命令快速执行 (`exec`)

无需登录交互终端，直接在本地对指定远程主机执行单条命令，实时流式返回标准输出/标准错误，并完整保留远程命令退出码（支持免引号参数；远程参数以 `-` 开头时需用 `--` 分隔）：

```bash
# 1. 快速查看 Docker 容器列表
ops exec oracle-sg -- docker ps

# 2. 查看磁盘或内存情况（可直接在本地通过管道符处理）
#    注意：远程参数以 - 开头时必须用 -- 分隔，否则该参数会被当成 ops 自身的标志
#    （ops exec oracle-sg df -h / 中的 -h 会触发帮助输出，命令根本不会执行）
ops exec oracle-sg -- df -h /
ops exec oracle-sg -- free -m
ops exec oracle-sg cat /var/log/nginx/access.log | grep 404 | wc -l

# 3. 设置超时时间（默认 60 秒，传 0 禁用超时）
ops exec oracle-sg "apt-get update" --timeout 120s

# 4. 批量并发执行（默认自动排除配置了 --skip-batch 的服务器）
ops exec -f all "uptime"
ops exec -f "provider=oracle" -j 10 "docker ps -q | wc -l"

# 5. 显式临时包含 skip-batch 服务器进行批量操作
ops exec -f all --include-skipped "uptime"
```

### `ops exec` 与 `ops ssh --exec` 的差异对照

两者都能在远端跑一条命令，但走的是**两条不同的技术路径**：`ops ssh --exec` 驱动系统 OpenSSH 客户端，`ops exec` 使用进程内 Go `x/crypto/ssh`。这些差异不是缺陷，而是两条路径各自存在的理由——按场景选：

| 维度 | `ops ssh <name> --exec "cmd"` | `ops exec <name> cmd ...` |
| --- | --- | --- |
| 传输层 | 系统 `ssh(1)` 子进程 | 进程内 Go `x/crypto/ssh` |
| 主机密钥 | ssh(1) 原生策略，与交互式登录共用同一份 `~/.ssh/known_hosts` | 严格校验：共用同一份 `~/.ssh/known_hosts`，未记录的主机直接报错（`OPSPULSE_TRUST_NEW_HOST_KEY=1` 可显式放行首次连接），已记录但密钥不匹配同样拒绝 |
| pty | 沿用 ssh(1) 规则：stdin 是终端即分配，`-- -T` 强制关闭 | 不分配 pty |
| stdout / stderr | 分离；横幅走 stderr，stdout 干净可直接进管道 | 合并为同一路输出（便于按时间顺序查看全量日志） |
| 退出码 | 原样透传 | 原样透传 |
| 超时 | 无 ops 层超时 | `--timeout`（默认 60s，传 `0` 禁用） |
| 老旧算法 | ssh(1) 原生支持，配合 `legacy-ssh` 标签受控降级 | Go 库可协商 `ssh-rsa` 主机密钥；仅提供 `ssh-dss` 的主机不可用 |
| 自动提权 | 不介入，命令以登录用户身份执行 | 远端为非 root 且 `sudo -n true` 可用时自动以 `sudo -E` 执行 |
| 连接复用 | ControlMaster 多路复用（Windows 上自动关闭） | 每次调用新建连接 |
| 命令封装 | 命令交给远端登录 shell，引号与管道按远端语义解释 | 整条命令经 base64 后交给远端 `bash -s`（行尾统一为 LF） |
| 批量执行 | 单台 | `-f all` / `-f provider=xxx` 批量并发（`-j` 控并发度，自动跳过 `skip_batch` 服务器） |

简言之：**要 pty 与交互性、需要 ssh(1) 原生算法兼容（老机器）时选 `ops ssh --exec`；要超时控制、自动提权、批量并发时选 `ops exec`。**

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
| **Linux** | FileZilla、Nautilus (GNOME Files)、xdg-open | FileZilla -> Nautilus -> xdg-open |

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

### WSL 下的私钥镜像目录

在 WSL 里唤起 Windows 侧 GUI 客户端时，私钥所在的 Linux 路径无法被 Windows 进程读取，Ops 会把私钥内容镜像到 Windows 用户目录 `%USERPROFILE%\.ssh\opspulse\`（WSL 内即 `/mnt/c/Users/<你>/.ssh/opspulse`，文件名沿用密钥名，如 `opspulse_<server>`），再把原生 Windows 路径交给客户端。

该镜像**刻意不会自动清理**：GUI 客户端是异步读取密钥的，可能在拉起它的进程退出之后才真正读取。需要清理时在 Windows 侧手动删除：

```powershell
Remove-Item -Recurse -Force "$env:USERPROFILE\.ssh\opspulse"
```

或在 WSL 中执行：

```bash
rm -rf /mnt/c/Users/<你>/.ssh/opspulse
```

注意 `/mnt/c` 上无法可靠地保留 POSIX `0600` 权限，该目录的实际访问控制由 Windows ACL 决定。

### 安全提示：密码会出现在命令行里

当服务器使用密码认证时，Ops 会构造 `sftp://user:password@host:port/path` 形式的 URL 并作为 GUI 客户端（WinSCP / Xftp / FileZilla 等）的命令行参数传递——本机上任何能列出进程的人都能读到这个密码。GUI 场景建议先用 `ops server setup-key <name>` 切到密钥认证：密钥以文件路径传递，密码不会进入命令行。

