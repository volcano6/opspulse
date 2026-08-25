# 日常服务器管理指南 (Server Operations)

OpsPulse 不仅是一键初始化与灾备迁移工具，更是日常高效管理多台 VPS 的核心入口。

---

## 1. 服务器列表与标签检索

### 添加带 Labels 的服务器

```bash
# 添加一台 Oracle 新加坡实例，附带 provider、region 与用途标签
opspulse server add oracle-sg \
  --host 168.138.1.1 \
  --user ubuntu \
  --key ~/.ssh/id_ed25519 \
  --labels provider=oracle,region=singapore,purpose=blog \
  --tags prod,web \
  --desc "生产环境博客主节点"
```

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

无需记忆每台服务器的 IP 地址、SSH 端口或对应私钥，直接通过服务器唯一标识名直连：

```bash
# 1. 一键交互式登录
opspulse ssh oracle-sg

# 2. 透传原生 SSH 客户端选项（使用 -- 分隔）
opspulse ssh oracle-sg -- -o StrictHostKeyChecking=no

# 3. 远程快速启动特定命令或 tmux
opspulse ssh oracle-sg -- tmux attach
```

> **设计优势**：
> - **Linux / macOS 平台**：采用系统底层进程替换（`syscall.Exec`），保证 100% 获得原生 PTY 交互体验（完美支持 vim、tmux、htop、Ctrl+C、窗口大小自适应 resize）。
> - **Windows 平台**：自动桥接标准终端管道，无缝唤起 OpenSSH。

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
