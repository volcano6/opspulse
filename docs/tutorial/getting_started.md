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

## 3. 第一步：添加纳管服务器

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

## 4. 第二步：查看与发现可用模板

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

## 5. 第三步：执行服务器初始化 (Bootstrap)

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
