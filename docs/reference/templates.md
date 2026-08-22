# 脚本模板开发指南

OpsPulse 使用声明式 Shell 脚本模板来执行服务器初始化、日常维护及自动化任务。

---

## 1. 内置官方模板

OpsPulse 直接通过 `go:embed` 将以下经过充分验证的官方模板嵌入到二进制中：

| 模板名称 | 操作系统支持 | 功能描述 | 主要执行动作 |
|----------|------------|---------|-------------|
| `base` | Ubuntu, Debian | 系统基础工具集 | 自动更新 apt 缓存，安装 `curl`, `wget`, `git`, `vim`, `htop`, `jq`, `ufw`, `fail2ban`, `ca-certificates` 等 |
| `docker` | Ubuntu, Debian | Docker CE 容器环境 | 配置 Docker 官方 apt 源，安装最新版 `docker-ce`, `docker-ce-cli`, `containerd.io`, `docker-compose-plugin` |
| `security` | Ubuntu, Debian | 安全与防火墙加固 | 智能识别当前活跃 SSH 端口并自动放行，开启 UFW 并放行 80/443，配置并启动 `fail2ban` 防暴破 |
| `restic` | Ubuntu, Debian | 备份工具链 | 安装官方最新版 `restic` 与 `rclone` 二进制包 |

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
| `description` | 字符串 | 否 | 模板功能简介，展示在 `opspulse template list` 中。 |

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
opspulse template list
```

### 同名优先覆盖机制
如果自定义目录中存在与内置模板同名的脚本（如 `~/.config/opspulse/templates/docker.sh`），**OpsPulse 将优先使用用户自定义的模板**，方便用户针对个人特殊需求对官方模板进行覆写。
