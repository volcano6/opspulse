# 日常服务器管理指南 (Server Operations)

OpsPulse 不仅是一键初始化与灾备迁移平台，更是日常高效管理多台 VPS 的核心入口。

---

## 1. 服务器列表与标签检索

### 添加带 Labels 的服务器

```bash
# 添加一台 Oracle 新加坡实例，附带 provider、region 与用途标签
opspulse server add oracle-sg \
  --host 168.138.1.1 \
  --user ubuntu \
  --key ~/Downloads/oracle.pem \
  --labels provider=oracle,region=singapore,purpose=blog \
  --tags prod,web \
  --desc "生产环境博客主节点"
```

> 🛡️ **私钥安全与自动连通性验证闭环**：
> - **位置检测与自动迁移**：当 `--key <path>` 指向 `~/.ssh/` 之外的目录（例如 `~/Downloads/` 或临时目录）时，OpsPulse 会交互式询问是否将密钥复制到 `~/.ssh/opspulse_<server_name>.pem` 并自动设置为 `0600` 权限（可用 `--no-copy-key` 跳过复制）。
> - **前置格式强校验**：自动校验文件内容是否为有效 SSH 私钥，拦截误选 `.pub` 公钥或非密钥文件。
> - **即时连通性验证与失败回滚**：`server add` 默认自动测试 SSH 连通性。若认证失败（如密钥选错），**会自动删除刚刚复制到 `~/.ssh/` 的失效私钥，且不保存错误服务器**，杜绝垃圾文件残留（离线服务器可通过 `--skip-test` 跳过验证）。
> - **生命周期自动清理**：通过 `server remove <name>` 删除服务器或 `server set <name> --key ...` 更换密钥时，自动清理 `~/.ssh/` 中对应的托管私钥文件。

### 增量更新与交互编辑

```bash
# 仅修改指定字段，其他配置保持不变；--key "" 可清除绑定密钥
opspulse server set oracle-sg --port 2222
opspulse server set oracle-sg --host 203.0.113.10 --key ~/.ssh/oracle-sg

# 使用 $VISUAL、$EDITOR 或系统默认编辑器打开完整清单并定位该服务器
opspulse server edit oracle-sg
```

`server edit` 在临时副本中编辑。编辑器正常退出后才校验 YAML、服务器字段和名称唯一性；校验失败或目标服务器被删除时，原 `servers.yaml` 保持不变。

### 多维筛选过滤 (`--filter`)

`opspulse server list --filter <condition>` 支持灵活的筛选模式：

```bash
# 1. 按 Label 键值对精确过滤
opspulse server list --filter provider=oracle

# 2. 按 Label 键名或取值模糊过滤
opspulse server list --filter singapore

# 3. 按 Tag 分组过滤
opspulse server list --filter prod

# 4. 按服务器名称匹配
opspulse server list --filter oracle-sg
```

输出示例：
```text
NAME          HOST          PORT   USER     AUTH                    LABELS                                                TAGS       DESCRIPTION
----          ----          ----   ----     ----                    ------                                                ----       -----------
oracle-sg     168.138.1.1   22     ubuntu   key (~/.ssh/id_ed25519) purpose=blog,provider=oracle,region=singapore         prod,web   生产环境博客主节点
```

---

## 2. 系统与硬件资源探测 (`server info`)

无需在远程服务器安装任何 Agent，OpsPulse 通过原生单次 SSH 会话聚合采集系统的关键硬件资源与运行时指标：

```bash
opspulse server info oracle-sg
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

## 3. 交互式原生 SSH 直连 (`ssh`)

无需记忆服务器地址、端口、密码或私钥，直接通过服务器名称进入终端：

```bash
# 1. 一键交互式登录；配置 password 时自动认证，不再二次询问
opspulse ssh oracle-sg

# 2. 将密码认证转换为专用密钥认证
opspulse server setup-key oracle-sg

# 3. 透传原生 SSH 客户端选项（使用 -- 分隔）
opspulse ssh oracle-sg -- -o StrictHostKeyChecking=no

# 4. 远程快速启动特定命令或 tmux
opspulse ssh oracle-sg -- tmux attach
```

`setup-key` 会生成 `~/.ssh/opspulse_<服务器名>`，使用清单中的密码将公钥幂等追加到远端 `~/.ssh/authorized_keys`，成功后将私钥路径写回 `servers.yaml`。它不会修改或删除远端密码，也不会关闭远端密码登录。

绑定 `key_path` 后，原生 SSH 会自动追加 `IdentitiesOnly=yes`，只提交该私钥，避免 ssh-agent 中多把密钥触发 `Too many authentication failures`。`server add --key` 支持补全 `id_*` 和 `*.pem` 私钥文件。

> **设计优势**：
> - **密钥模式（Linux / macOS）**：采用系统底层进程替换（`syscall.Exec`），保证原生 PTY 交互体验。
> - **密码模式及 Windows**：桥接标准终端，并通过受限临时文件向 OpenSSH `SSH_ASKPASS` 传递密码；密码不出现在命令参数或环境变量值中。

---

## 4. 远程单命令快速执行 (`exec`)

无需登录交互终端，直接在本地对指定远程主机执行单条命令，实时流式返回标准输出/标准错误，并完整保留远程命令退出码：

```bash
# 1. 快速查看 Docker 容器列表
opspulse exec oracle-sg "docker ps --format 'table {{.Names}}\t{{.Status}}'"

# 2. 查看磁盘或内存情况（可直接在本地通过管道符处理）
opspulse exec oracle-sg df -h /
opspulse exec oracle-sg "cat /var/log/nginx/access.log" | grep 404 | wc -l

# 3. 设置超时时间（默认 60 秒，传 0 禁用超时）
opspulse exec oracle-sg "apt-get update" --timeout 120s
```

---

## 5. SFTP 文件与目录传输 (`upload` / `download`)

基于高性能 SFTP 子系统，直接在本地与远程服务器之间上传或下载单个文件或递归目录，自动创建缺失的父级目录并保持权限：

### 文件与目录上传 (`upload`)

```bash
# 1. 单个文件上传
opspulse upload oracle-sg ./nginx.conf /etc/nginx/nginx.conf

# 2. 递归目录上传（必须加 -r / --recursive 参数）
opspulse upload oracle-sg ./configs/ /opt/app/configs/ --recursive
```

### 文件与目录下载 (`download`)

```bash
# 1. 单个文件下载到本地
opspulse download oracle-sg /var/log/nginx/error.log ./error.log

# 2. 递归目录下载到本地
opspulse download oracle-sg /var/data/ghost/ ./ghost-backup/ --recursive
```
