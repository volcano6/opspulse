# 脚本模板开发指南

OpsPulse 使用声明式 Shell 脚本模板来执行服务器初始化、日常维护及自动化任务。

---

## 1. 内置官方模板

OpsPulse 直接通过 `go:embed` 将以下经过充分验证的官方模板嵌入到二进制中：

| 模板名称 | 操作系统支持 | 功能描述 | 主要执行动作 |
|----------|------------|---------|-------------|
| `base` | Ubuntu, Debian | 系统基础工具集 | 自动更新 apt 缓存，安装常用工具与排障套件，开启 TCP BBR 拥塞控制 |
| `bbr` | Ubuntu, Debian | 开启 TCP BBR 拥塞控制 | 独立开启 Linux TCP BBR 与 fq 排队规则（写入 `/etc/sysctl.d/99-bbr.conf`） |
| `security` | Ubuntu, Debian | 安全与防火墙加固 | 智能识别当前活跃 SSH 端口并自动放行，放行 Web 80/443，开启 UFW 与 fail2ban 防暴破 |
| `firewall-ports` | Ubuntu, Debian | 开放自定义防火墙端口 | 按参数灵活批量放行端口（如 `-t firewall-ports:80,443,8080/tcp,51820/udp`） |
| `docker` | Ubuntu, Debian | Docker CE 容器环境 | 安装 Docker CE 与 Compose 插件，支持国内镜像源自动回退与 daemon.json 日志轮转配置 |
| `nginx` | Ubuntu, Debian | Nginx Web 服务器 | 配置官方源安装最新稳定版 Nginx，开机自启并放行 80/443 端口 |
| `caddy` | Ubuntu, Debian | Caddy Web 服务器 | 安装官方 Caddy 并设置开机自启，自动申请 HTTPS 证书 |
| `golang` | Ubuntu, Debian | Go 语言开发环境 | 从官方/国内镜像下载安装指定或最新稳定版 Go，自动配置 PATH 与软链接 |
| `uv` | Ubuntu, Debian | Astral uv Python 工具链 | 安装极速 Python 包与项目管理工具 uv/uvx 至 `/usr/local/bin` |
| `restic` | Ubuntu, Debian | 备份工具链 | 安装 `restic` 与 `rclone` 二进制包，为 `ops backup` 提供执行基础 |
| `swap` | Ubuntu, Debian | 零停机 Swap 扩容/调整 | 默认创建 2GB（可传参调整，如 `-t swap:4`），双文件热切换，优化 swappiness |
| `timezone` | Ubuntu, Debian | 系统时区与时间同步 | 默认设置 `Asia/Shanghai`（支持传参如 `-t timezone:UTC`），开启 NTP 自动授时 |
| `tmux` | Ubuntu, Debian | 终端复用与精巧配置 | 安装 tmux，配置鼠标滚动支持、10000 行历史回滚与 Dracula 主题状态栏 |
| `zsh-starship` | Ubuntu, Debian | 现代终端与美化 | 安装 Zsh + Starship 提示符，配置命令自动补全与语法高亮插件 |
| `clean` | Ubuntu, Debian | 磁盘与资源清理 | 清理 apt 缓存、7天前 journalctl 日志与无用 Docker 资源 |
| `upgrade` | Ubuntu, Debian | 系统包安全更新 | 无人值守升级系统软件包与安全补丁，检测内核更新并提示重启 |
| `cluster-check` | Ubuntu, Debian | 节点指标快捷巡检 | 单框直观输出节点主机名、IP、负载、内存使用与根分区磁盘空间 |

---

## 2. YAML Frontmatter 元数据语法

每个脚本模板可在文件头部通过 `# ---` 区块定义可选的元数据声明：

```bash
#!/bin/bash
# ---
# name: nodejs-setup
# version: 1
# os: [ubuntu, debian]
# description: 通过 NodeSource 源安装 Node.js 22 LTS
# ---
set -euo pipefail

echo "=== 安装 Node.js LTS ==="
curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
apt-get install -y nodejs
node -v
npm -v
```

### Frontmatter 字段说明

| 字段 | 类型 | 是否必填 | 说明 |
|------|------|----------|------|
| `name` | 字符串 | 否 | 模板唯一标识名。若未填写，默认使用去除 `.sh` 后的文件名。 |
| `version` | 整数 | 否 | 模板版本号（默认为 `1`）。 |
| `os` | 字符串列表 | 否 | 支持的目标操作系统列表（例如 `[ubuntu, debian]`）。 |
| `description` | 字符串 | 否 | 模板功能简介，展示在 `ops template list` 中。 |

---

## 3. 自定义脚本模板

你可以将自己的 `.sh` 脚本放置在用户自定义模板目录下：

```bash
# 默认自定义模板路径：
# Linux:   ~/.config/opspulse/templates/
# macOS:   ~/Library/Application Support/opspulse/templates/
# Windows: %APPDATA%/opspulse/templates/

mkdir -p ~/.config/opspulse/templates

cat <<'EOF' > ~/.config/opspulse/templates/caddy.sh
#!/bin/bash
# ---
# name: caddy
# version: 1
# os: [ubuntu, debian]
# description: 安装并配置 Caddy 现代 Web 服务器
# ---
apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | tee /etc/apt/sources.list.d/caddy-stable.list
apt-get update
apt-get install -y caddy
caddy version
EOF
```

运行查看命令验证自定义模板是否已被正确识别：
```bash
ops template list
```

### 同名优先覆盖机制
如果自定义目录中存在与内置模板同名的脚本（如 `~/.config/opspulse/templates/docker.sh`），**OpsPulse 将优先使用用户自定义的模板**，方便用户针对个人特殊需求对官方模板进行覆写。
