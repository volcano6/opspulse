<!-- 由 `make docs-gen` 从 cmd/opspulse 的 cobra 命令树生成，请勿手工编辑。 -->

# 命令行参考 (CLI Reference)

本页是 `ops` 全部命令与 flag 的权威清单，由 `cmd/opspulse` 的 cobra 命令树直接生成，
随代码更新。`opspulse` 是 `ops` 的别名，两者完全等价。重新生成：`make docs-gen`。

---

## 1. 命令总览 (Command overview)

| 命令 | 说明 |
| --- | --- |
| `ops` | 个人开发者的自托管服务器自动化与备份编排 |
| `ops 1p` | 把本机 SSH 凭据备份到 1Password，或还原到新机器 |
| `ops 1p backup` | 把本机全部凭据与整份 servers.yaml 上传到 1Password |
| `ops 1p config` | 查看或修改记住的 1Password 保险库与账号 |
| `ops 1p doctor` | 只读自检本机 1Password 集成 |
| `ops 1p restore [server...]` | 把 1Password 里的凭据写回本地磁盘 |
| `ops 1p status` | 查看哪些服务器有本地凭据、哪些已备份 |
| `ops add <name> [target]` | 新增或更新清单中的服务器 |
| `ops asset` | 管理业务资产 |
| `ops asset add <id>` | 注册或更新业务资产 |
| `ops asset list` | 列出全部已配置资产 |
| `ops asset remove <id>` | 从配置中删除资产 |
| `ops asset show <id>` | 查看指定资产的详细信息 |
| `ops backup` | 管理与执行备份作业 |
| `ops backup history <job-name>` | 查看指定备份作业的历史执行记录 |
| `ops backup list` | 列出全部已配置备份作业 |
| `ops backup run <job1,job2... \| all \| server:container>` | 执行备份作业或直接备份容器 |
| `ops backup snapshots <job-name>` | 查询并列出备份作业的远端快照 |
| `ops backup status` | 查看全部备份作业的最新状态 |
| `ops bootstrap <server1,server2...>` | 用指定脚本模板初始化服务器 |
| `ops completion [bash\|zsh\|fish\|powershell]` | 生成 Shell 补全脚本或自动安装 |
| `ops cp [flags] <source> <destination>` | 经 SFTP 在本地与远端服务器之间拷贝文件或目录 |
| `ops daemon` | 运行后台调度器守护进程，自动执行定时备份 |
| `ops doctor [flags]` | 巡检集群健康（SSH 延迟、磁盘占用、Docker 状态） |
| `ops exec [server] <command...>` | 在远端服务器上执行命令 |
| `ops export` | 导出配置与工具集成 |
| `ops export ssh-config` | 把受管服务器导出为 OpenSSH 配置（供 VS Code、Cursor 与原生 ssh 使用） |
| `ops info <name>` | 采集服务器的系统、硬件与 Docker 状态（等价于 ops server info） |
| `ops logs <server> <container>` | 查看或跟随远端 Docker 容器日志 |
| `ops ls` | 列出所有已配置的服务器（等价于 ops server list） |
| `ops notify` | 管理与测试告警通知渠道 |
| `ops notify list` | 列出全部已配置通知渠道 |
| `ops notify test [channel-name]` | 发送测试通知验证 Webhook 投递 |
| `ops ps <server>` | 列出远端服务器上的 Docker 容器 |
| `ops restore` | 从 restic 备份快照还原数据 |
| `ops restore history [job-name]` | 查看还原执行历史记录 |
| `ops restore run <job-name>` | 从备份快照执行还原操作 |
| `ops server` | 管理服务器清单 |
| `ops server add <name> [target]` | 新增或更新清单中的服务器 |
| `ops server edit <name>` | 编辑服务器清单并在保存前校验 |
| `ops server info <name>` | 采集服务器的系统、硬件与 Docker 状态 |
| `ops server list` | 列出所有已配置的服务器 |
| `ops server remove <name>` | 从服务器清单中删除服务器 |
| `ops server set <name>` | 增量更新已有服务器的指定字段 |
| `ops server setup-key <name>` | 用已配置的密码生成并安装 SSH 密钥 |
| `ops server test <name>` | 测试服务器的 SSH 连通性 |
| `ops sftp [server] [flags]` | 唤起 GUI SFTP 客户端（WinSCP/Xftp/FileZilla）或 CLI 管理远端文件 |
| `ops ssh [name] [flags] [-- <ssh_args...>]` | 建立到服务器的交互式 SSH 终端会话 |
| `ops template` | 管理并查看脚本模板 |
| `ops template list` | 列出全部可用模板 |
| `ops template show <name>` | 查看脚本模板的内容与元数据 |
| `ops test <name>` | 测试服务器的 SSH 连通性（等价于 ops server test） |
| `ops version` | 打印版本信息 |

---

## 2. 命令与 flag 详情 (Commands and flags)

### `ops`

别名：`opspulse`

Ops —— 自托管服务器自动化、备份编排与安全运维。

环境变量：
```
  OPSPULSE_HOME                覆盖 OpsPulse 主目录（配置与数据）。
  OPSPULSE_OP_PATH             强制指定 'ops 1p' 驱动的 1Password CLI 可执行文件。
  OPSPULSE_KNOWN_HOSTS         为内置 SSH 客户端指定其他 known_hosts 文件。
  OPSPULSE_TRUST_NEW_HOST_KEY  设为 1，首次连接时接受未知主机密钥。
  OP_VAULT                     默认 1Password 保险库，覆盖已记住的值。
  OP_ACCOUNT                   默认 1Password 账户，覆盖已记住的值。
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--debug` | - | `false` | 输出详细的调试日志 |

### `ops 1p`

别名：`ops 1password`、`ops onepassword`

1Password 是备份与跨机器同步的目标，不是运行时依赖。

```
  ops 1p backup             把本机全部密钥/密码与整份 servers.yaml 上传
  ops 1p restore            把它们写回本地磁盘（并还原 servers.yaml）
  ops 1p status             查看哪些服务器有本地凭据
  ops 1p config             查看或修改记住的保险库与账号
  ops 1p doctor             端到端自检整条链路，不改动任何东西
```

凭据平时就放在本地磁盘：servers.yaml 里存的是密钥路径或明文密码，
'ops ssh' / 'ops exec' / 'ops cp' 直接读取，不与 1Password 发生任何往返。
正常连接过程中不会运行这里的任何东西，这正是那些命令从不弹授权框的原因。

密钥存放在标题为 opspulse_<server>_key 的 Login 条目里，位于一个自定义的
concealed 字段中；密码存放在标题为 opspulse_<server>_password 的 Login 条目里。
servers.yaml 本身作为共享条目 opspulse_inventory 备份。

通常你完全不必指定保险库：OpsPulse 用你通过 'ops 1p config --vault <name>'
记住的那个，其次是 $OP_VAULT，否则就是该账号唯一可见的保险库。
账号同理，$OP_ACCOUNT 优先于记住的值。

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--account` | - | (空) | 1Password 账号（登录地址或 ID）；会被记住供后续运行使用 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops 1p backup`

把本机的服务器清单与它持有的每一把私钥备份到 1Password。

整台机器装在一个以本机名命名的 Secure Note 里（opspulse_inventory_<hostname>），
这正是让备份只花三次 op 调用、而不是每台服务器一次的原因。它刻意是无条件的：
不做服务器选择，也没有跳过列表。

条目先读后写，本机已经读不出来的私钥会从上一份备份里沿用，而不是被丢掉——
本地文件没了之后，那份副本就是唯一的了。因其他原因导致的读取失败会中止备份，
而不是盲目覆盖。

servers.yaml 不会被改写。本地磁盘始终是唯一真相源，所以备份绝不会改变
'ops ssh' 的连接方式，也绝不会把一台本来能连的服务器变成依赖 1Password 解锁的服务器。

```
  ops 1p backup
  ops 1p backup --vault Private
```

仍然持有 'op://' 引用的服务器会被直接拒绝：上传它等于把一条陈旧引用推进备份。
先跑 'ops 1p restore' 把它迁移成本地凭据。

每台机器备份到各自的条目，所以两台机器永远不会互相覆盖，还原时再把它们并起来。
因此从备份里删除一台服务器只能手工做：先在本地删掉，再备份一次——
其他机器会一直保留它，直到它们也备份一次。

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--vault` | - | (空) | 要备份到的保险库（默认：记住的设置，其次 $OP_VAULT，最后是唯一可访问的保险库） |

继承的持久 flag：`--account`（定义于 `ops 1p`）、`--debug`（定义于 `ops`）。

### `ops 1p config`

查看或修改 OpsPulse 记住的 1Password 默认值。

不带选项时打印当前生效的目标，以及当前账号能看到的账号与保险库。
带选项时记录一个默认值，让 'ops 1p backup' 不再每次都要求 --vault/--account。

传 --offline 可以在不联系 CLI 的情况下读取或修改记住的默认值，
保险库暂时解不开锁时正需要它。

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--account` | - | (空) | 把这个账号记为默认值 |
| `--offline` | - | `false` | 只读写本地配置，不联系 1Password CLI |
| `--unset` | - | `false` | 忘掉记住的保险库与账号 |
| `--vault` | - | (空) | 把这个保险库记为默认目标 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops 1p doctor`

以只读方式把整条 1Password 链路走一遍，并指出断在哪里。

该命令按顺序检查：'op' 可执行文件是否存在、是哪个构建（WSL 下必须是 Windows 版），
账号是否可见，保险库能否列出以及会选哪一个，本机的备份条目是否存在、
里面装了什么，最后是 servers.yaml 是否还指向尚未还原到本地的凭据。

不写入任何东西：不改保险库、不改账号、也不改 servers.yaml 里的任何条目。
每一步都报告为 ok / warn / fail，只要有一个 fail 就以非零状态退出，
因此可以直接当预检用。

用 --offline 只跑不需要 1Password 往返的检查。

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--offline` | - | `false` | 只跑不需要 1Password 往返的检查 |
| `--vault` | - | (空) | 要检查的保险库（默认：记住的设置，其次 $OP_VAULT，最后是唯一可访问的保险库） |

继承的持久 flag：`--account`（定义于 `ops 1p`）、`--debug`（定义于 `ops`）。

### `ops 1p restore [server...]`

把 1Password 里的凭据还原到本机。

不带参数时这是完整的脱困通道：先从各机器的备份条目还原 servers.yaml，
再把每台服务器的密钥写到 ~/.ssh/opspulse_<server>、把每个密码写进 servers.yaml。
一台新机器装好 1Password CLI 后，需要的就是这些。

```
  ops 1p restore               # 清单与全部凭据
  ops 1p restore web db-01     # 只还原这几台服务器的凭据
  ops 1p restore --yes         # 无人值守
```

带参数时只还原点名服务器的凭据，servers.yaml 保持不动。名字不在 servers.yaml 里
是报错而不是静默跳过，因为最常见的成因就是清单还没还原就先还原凭据。

密码只能以明文形式回来，所以只要本次还原会写入密码，OpsPulse 在写任何东西之前
都会先要求确认。在非交互 shell 里命令会直接拒绝而不是挂住，除非 --yes 事先给出答案。

本地密钥文件只在持有另一把密钥时才会被替换。比对按公钥进行，所以 1Password
以另一种格式返回的密钥会被认作同一把密钥，而不是被当成冲突；--force 可强制覆盖。

servers.yaml 里仍然持有 'op://' 引用的服务器会在还原过程中迁移为本地凭据。
这条兼容路径是临时的。

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--force` | - | `false` | 即使本地密钥文件持有另一把密钥也强制覆盖 |
| `--prefer-local` | - | `false` | 所有清单冲突都以本机的 servers.yaml 为准 |
| `--prefer-remote` | - | `false` | 所有清单冲突都以 1Password 备份为准 |
| `--vault` | - | (空) | 要从中还原的保险库（默认：记住的设置，其次 $OP_VAULT，最后是唯一可访问的保险库） |
| `--yes` | `-y` | `false` | 不询问确认就把明文密码写入 servers.yaml |

继承的持久 flag：`--account`（定义于 `ops 1p`）、`--debug`（定义于 `ops`）。

### `ops 1p status`

查看每台服务器的凭据放在哪儿。

默认离线：只读 servers.yaml，因此从不联系 1Password、从不弹授权框。
传 --remote 会额外查询保险库，看哪些服务器有备份，这一步需要授权。

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--filter` | `-f` | (空) | 按 label（key=val）、tag 或名称筛选服务器 |
| `--remote` | - | `false` | 额外查询 1Password 里哪些服务器有备份（需要授权） |

继承的持久 flag：`--account`（定义于 `ops 1p`）、`--debug`（定义于 `ops`）。

### `ops add <name> [target]`

新增或更新 servers.yaml 中的服务器。

目标可用位置参数 [user@]host[:port] 指定，也可通过 flag 指定。
默认用户为 root，默认 SSH 端口为 22。

新增时服务器名会被规范化：下划线改写为中划线，"web_1" 保存为 "web-1"，
避免同一台机器以两个肉眼难分的名字在清单里出现两次。已存在的条目不会被改名。

示例：
```
  # 静默输入密码添加服务器，并自动注入公钥
  ops add vps-1 1.2.3.4

  # 指定自定义用户与端口
  ops add prod ubuntu@1.2.3.4:2222

  # 指定私钥
  ops add backup 1.2.3.4 -i ~/.ssh/id_ed25519

  # 使用 flag 添加
  ops add node-1 --host 10.0.0.1 --labels env=prod,provider=racknerd
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--desc` | `-d` | (空) | 服务器描述 |
| `--host` | - | (空) | 服务器 IP 或主机名 |
| `--identity` | `-i` | (空) | 私钥文件路径 |
| `--jump-host` | `-J` | (空) | 清单中的跳板机服务器名（Bastion / Jump Host） |
| `--key` | `-k` | (空) | 私钥文件路径（-i 的别名） |
| `--labels` | `-l` | (空) | 逗号分隔的 key=value Label（如 provider=oracle,region=sg） |
| `--no-copy-key` | - | `false` | 私钥位于 ~/.ssh/ 之外时不提示复制到该目录 |
| `--password` | - | (空) | SSH 密码（可选；未指定私钥且未提供密码时交互式输入）。注意：在此传入的密码会以明文记录在 Shell 历史中，建议改用交互式提示或 ops server setup-key |
| `--port` | `-p` | `22` | SSH 端口 |
| `--skip-batch` | - | `false` | 将该服务器排除在隐式批量操作之外（如 ops exec -f all、ops doctor） |
| `--skip-test` | - | `false` | 添加服务器时跳过 SSH 连通性测试 |
| `--tags` | - | (空) | 逗号分隔的标签（如 prod,web） |
| `--user` | `-u` | `root` | SSH 用户名 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops asset`

定义、查看并管理有状态的业务资产（Docker Compose 项目、Volume、数据库、目录、文件），
每个资产一个稳定 ID，备份与跨机还原时按 ID 引用。

资产记录保存在 $XDG_CONFIG_HOME/opspulse/assets.yaml。

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops asset add <id>`

注册一个有状态的业务资产，并分配一个稳定 ID，供备份与还原按 ID 引用。

支持的类型：docker_compose、volume、database、directory、file

示例：
```
  ops asset add blog-compose --type docker_compose --source /opt/blog --desc "Ghost blog"
  ops asset add blog-mysql --type database --source /var/lib/mysql --engine mysql --container blog-db
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--container` | - | (空) | Docker 容器名——仅对 database 类型有效 |
| `--desc` | `-d` | (空) | 资产描述 |
| `--engine` | - | (空) | 数据库引擎（mysql、postgres）——仅对 database 类型有效 |
| `--excludes` | - | (空) | 逗号分隔的 glob 排除规则 |
| `--source` | - | (空) | 服务器上的来源路径（必填） |
| `--type` | - | (空) | 资产类型（docker_compose、volume、database、directory、file） |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops asset list`

列出全部已配置资产

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops asset remove <id>`

别名：`ops asset rm`、`ops asset delete`

从 assets.yaml 中删除一条资产记录。

若仍有备份作业在 assets: 里引用该资产，会打印一条警告；那个作业会在下次执行时失败，
但这不是保留一条失效资产记录的理由。删除从不阻塞，也从不追问。

示例：
```
  ops asset remove old-mysql
```

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops asset show <id>`

查看指定资产的详细信息

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops backup`

定义、查看、执行并监控跨服务器的 restic 备份作业。
每次备份的结构化指标、快照 ID 与历史日志持久化记录到 SQLite。

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops backup history <job-name>`

查看指定备份作业的历史执行记录

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--limit` | `-n` | `20` | 最多显示的历史记录条数 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops backup list`

列出全部已配置备份作业

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops backup run <job1,job2... | all | server:container>`

执行一个或多个已配置的备份作业，或直接备份单个容器。

示例：
```
  ops backup run blog-backup                # 执行单个作业
  ops backup run blog-backup,db-backup -j 2 # 并发执行多个作业，每次两个
  ops backup run all -j unlimited           # 取消默认并发上限
  ops backup run vps-1:blog-db --as blog    # 备份 vps-1 上的容器 blog-db，作业名为 "blog"
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--as` | - | (空) | 重命名生成的 Compose 与备份作业中的容器（使用 <server>:<container> 时） |
| `--dry-run` | - | `false` | 预演执行，不真正运行 restic（不支持 <server>:<container> 目标） |
| `--parallel` | `-j` | (空) | 最大并发作业数（默认 5，'unlimited' 表示不设上限） |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops backup snapshots <job-name>`

查询并列出备份作业的远端快照

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops backup status`

查看全部备份作业的最新状态

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops bootstrap <server1,server2...>`

通过 SSH 依次执行一系列脚本模板，初始化并配置一台或多台服务器。
日志实时输出到终端，并保存到 $XDG_DATA_HOME/opspulse/logs/。

示例：
```
  ops bootstrap web-01 -t base,docker     # 对一台服务器应用两个模板
  ops bootstrap web-01,db-01 -t docker    # 把同一个模板应用到多台服务器
  ops bootstrap local -t base --dry-run   # 在本机预演这次执行
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--continue-on-error` | - | `false` | 出错后继续执行剩余的模板/服务器 |
| `--dry-run` | - | `false` | 预演执行，不建立 SSH 连接 |
| `--templates` | `-t` | `[]` | 要执行的模板列表，逗号分隔（例如 -t base,security,docker 或 -t base -t docker） |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops completion [bash|zsh|fish|powershell]`

为 Ops（ops）生成 Shell 自动补全脚本，或用 --install 选项把它们
自动写入你的 Shell 配置文件。

支持的 Shell：bash、zsh、fish、powershell。

示例：
```
  # 把自动补全直接安装进你的 Shell profile
  ops completion --install

  # 把补全脚本生成到标准输出
  ops completion bash
  ops completion zsh
  ops completion fish
  ops completion powershell
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--install` | `-i` | `false` | 自动把补全脚本安装进当前用户的 Shell profile |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops cp [flags] <source> <destination>`

在本地与受管远端服务器之间拷贝文件或目录。
远端位置必须以 '<server>:' 前缀标识。

示例：
```
  ops cp ./dist vps-1:/var/www/               # 上传到远端服务器
  ops cp vps-1:/var/log/nginx/access.log .    # 从远端服务器下载
  ops cp -r ./src vps-1:/tmp/src              # 递归上传目录
  ops cp -r vps-1:/var/log ./logs             # 递归下载目录
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--recursive` | `-r` | `false` | 递归拷贝目录 |
| `--timeout` | `-T` | `1m0s` | SFTP 连接超时 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops daemon`

启动 OpsPulse 调度器守护进程，按 $XDG_CONFIG_HOME/opspulse/backups.yaml 中配置的
cron 表达式执行备份作业。

作业执行完毕或出现故障时，按 $XDG_CONFIG_HOME/opspulse/notifications.yaml 的配置
自动分发告警通知。

收到 SIGINT 与 SIGTERM 信号时优雅退出，等待正在执行中的作业完成。

示例：
```
  ops daemon          # 运行调度器直到被中断
  ops daemon --once   # 按序执行全部定时作业一次后退出
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--once` | - | `false` | 按序执行全部定时备份作业一次后立即退出 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops doctor [flags]`

对清单中的服务器执行非侵入式健康巡检。
检查 SSH 连通性、根分区磁盘占用与 Docker 守护进程状态。

示例：
```
  ops doctor                       # 巡检全部服务器
  ops doctor -f "provider=oracle"  # 巡检指定集群
  ops doctor -j 10                 # 每次 10 台（默认 5，'unlimited' 不设上限）
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--filter` | `-f` | `all` | 过滤目标服务器（如 'all'、'provider=oracle' 或 tag） |
| `--include-skipped` | - | `false` | 健康巡检时包含配置了 skip_batch 的服务器 |
| `--parallel` | `-j` | (空) | 服务器并发探测上限（默认 5，'unlimited' 不设上限） |
| `--timeout` | `-T` | `15s` | 单台服务器探测超时 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops exec [server] <command...>`

在指定远端服务器上执行任意 shell 命令，或经过滤器在多台服务器上批量执行。

flag 必须写在服务器名之前：服务器名之后的一切都是远程命令，因此
'ops exec vps-1 df -h /' 可直接使用。命令之前仍可加 '--'，用于传递自身包含
--filter 的命令。

示例：
```
  ops exec vps-1 uptime                           # 单台服务器
  ops exec vps-1 df -h /                          # -h 属于远程命令
  ops exec --filter all "uptime"                  # 全部服务器并发执行
  ops exec --filter "provider=racknerd" "df -h"   # 按 label 或 tag 过滤
  ops exec -f all -j 10 "docker ps -q | wc -l"    # 每次 10 台（默认 5，'unlimited' 不设上限）
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--filter` | `-f` | (空) | 过滤目标服务器（如 'all'、'provider=racknerd' 或 tag） |
| `--include-skipped` | - | `false` | 批量执行时包含配置了 skip_batch 的服务器 |
| `--parallel` | `-j` | (空) | 服务器并发执行上限（默认 5，'unlimited' 不设上限） |
| `--timeout` | `-T` | `1m0s` | 命令执行超时（传 0 禁用超时） |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops export`

导出 Ops 配置、清单，以及面向 VS Code 与 Cursor 的 OpenSSH 配置等工具集成。

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops export ssh-config`

把受管服务器渲染为 OpenSSH 配置格式，与 VS Code Remote-SSH、Cursor、GoLand
以及原生 'ssh' 终端命令无缝集成。

默认把渲染结果打印到标准输出。
用 --write 自动且幂等地更新 ~/.ssh/config。
--file 只在配合 --write 时生效。

示例：
```
  # 把 SSH 配置打印到标准输出
  ops export ssh-config

  # 直接写入 ~/.ssh/config（幂等，保留自定义 Host）
  ops export ssh-config --write

  # 把筛选后的服务器写入自定义路径
  ops export ssh-config --write --file ~/.ssh/config.opspulse --filter env=prod
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--file` | - | (空) | 目标 SSH 配置文件路径（默认 ~/.ssh/config；需配合 --write） |
| `--filter` | `-f` | (空) | 按 label（key=val）、tag 或名称筛选服务器 |
| `--write` | `-w` | `false` | 直接写入 SSH 配置文件（幂等） |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops info <name>`

采集服务器的系统、硬件与 Docker 状态（等价于 ops server info）

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops logs <server> <container>`

通过 SSH 查看或实时跟随远端服务器上 Docker 容器的日志。

示例：
```
  ops logs vps-1 nginx                    # 最近 100 行日志
  ops logs vps-1 nginx --tail 50          # 最近 50 行日志
  ops logs vps-1 nginx --follow           # 实时跟随（Ctrl+C 退出）
  ops logs vps-1 nginx --timestamps       # 每行前面加上时间戳
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--follow` | - | `false` | 实时跟随日志输出 |
| `--tail` | `-n` | `100` | 从日志末尾显示的行数 |
| `--timestamps` | - | `false` | 显示时间戳 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops ls`

列出所有已配置的服务器（等价于 ops server list）

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--filter` | `-f` | (空) | 按 Label（key=value）、Tag 或名称筛选服务器 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops notify`

查看已配置的 Webhook 通知渠道并验证告警投递。

配置文件：$XDG_CONFIG_HOME/opspulse/notifications.yaml

示例：
```
  ops notify list                 # 列出全部已配置渠道
  ops notify test                 # 向全部渠道发送测试事件
  ops notify test ops-alerts      # 仅向指定渠道发送测试事件
```

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops notify list`

列出全部已配置通知渠道

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops notify test [channel-name]`

向已配置的通知渠道发送一条测试事件载荷，验证投递是否正常。
提供 [channel-name] 时只测试该渠道，否则测试全部渠道。

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops ps <server>`

通过 SSH 快速列出指定远端服务器上的 Docker 容器。
输出等价于远端 docker ps（容器 ID、镜像、命令、状态、端口、名称）。

示例：
```
  ops ps vps-1            # 运行中的容器
  ops ps vps-1 -a         # 连已停止的一起列
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--all` | `-a` | `false` | 显示全部容器（默认只显示运行中的） |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops restore`

执行基于 restic 备份快照的还原操作，支持跨机迁移、路径重映射、
单资产精准还原与 dry-run 预演。

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops restore history [job-name]`

显示还原操作的历史记录，可按备份作业名筛选。
展示状态、快照 ID、源/目标服务器、耗时与时间戳。

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--limit` | `-n` | `20` | 最多显示的历史记录条数 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops restore run <job-name>`

将 restic 备份快照中的文件还原到原始服务器或另一台服务器。

示例：
```
  # 还原最新快照到原始服务器和原始路径
  ops restore run blog-backup

  # 还原指定快照
  ops restore run blog-backup --snapshot abc12345

  # 跨机迁移：还原到新 VPS
  ops restore run blog-backup --target-server new-vps --target-path /data/blog

  # 单资产精准还原
  ops restore run blog-backup --asset blog-mysql

  # 预演文件列表，不真正还原
  ops restore run blog-backup --dry-run
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--as` | - | (空) | 在目标服务器上重命名容器/服务项目名 |
| `--asset` | - | (空) | 仅还原指定资产（按资产 ID） |
| `--dry-run` | - | `false` | 预演模式：仅列出文件，不执行实际还原 |
| `--no-start` | - | `false` | 还原后不自动启动：仅解压文件，不拉起容器也不灌库 |
| `--snapshot` | - | `latest` | 用于还原的快照 ID（'latest' 表示最新快照） |
| `--target-path` | - | (空) | 覆盖还原目标路径以实现路径重映射（默认使用快照中的原始路径） |
| `--target-server` | - | (空) | 跨机迁移的目标服务器（默认与源相同） |
| `--yes` | `-y` | `false` | 跳过执行前的交互式确认 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops server`

新增、列出、查看、测试连通性与删除 servers.yaml 中托管的服务器。

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops server add <name> [target]`

新增或更新 servers.yaml 中的服务器。

目标可用位置参数 [user@]host[:port] 指定，也可通过 flag 指定。
默认用户为 root，默认 SSH 端口为 22。

新增时服务器名会被规范化：下划线改写为中划线，"web_1" 保存为 "web-1"，
避免同一台机器以两个肉眼难分的名字在清单里出现两次。已存在的条目不会被改名。

示例：
```
  # 静默输入密码添加服务器，并自动注入公钥
  ops add vps-1 1.2.3.4

  # 指定自定义用户与端口
  ops add prod ubuntu@1.2.3.4:2222

  # 指定私钥
  ops add backup 1.2.3.4 -i ~/.ssh/id_ed25519

  # 使用 flag 添加
  ops add node-1 --host 10.0.0.1 --labels env=prod,provider=racknerd
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--desc` | `-d` | (空) | 服务器描述 |
| `--host` | - | (空) | 服务器 IP 或主机名 |
| `--identity` | `-i` | (空) | 私钥文件路径 |
| `--jump-host` | `-J` | (空) | 清单中的跳板机服务器名（Bastion / Jump Host） |
| `--key` | `-k` | (空) | 私钥文件路径（-i 的别名） |
| `--labels` | `-l` | (空) | 逗号分隔的 key=value Label（如 provider=oracle,region=sg） |
| `--no-copy-key` | - | `false` | 私钥位于 ~/.ssh/ 之外时不提示复制到该目录 |
| `--password` | - | (空) | SSH 密码（可选；未指定私钥且未提供密码时交互式输入）。注意：在此传入的密码会以明文记录在 Shell 历史中，建议改用交互式提示或 ops server setup-key |
| `--port` | `-p` | `22` | SSH 端口 |
| `--skip-batch` | - | `false` | 将该服务器排除在隐式批量操作之外（如 ops exec -f all、ops doctor） |
| `--skip-test` | - | `false` | 添加服务器时跳过 SSH 连通性测试 |
| `--tags` | - | (空) | 逗号分隔的标签（如 prod,web） |
| `--user` | `-u` | `root` | SSH 用户名 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops server edit <name>`

编辑服务器清单并在保存前校验。

目标服务器的清单内容会在 $VISUAL 或 $EDITOR 中打开（两者都未设置时用 vi）。
只有在编辑后的文档能正常解析、且仍然包含该服务器时才会替换清单，
因此格式错误的编辑不会破坏 servers.yaml。

示例：
```
  ops server edit blog-vps
  EDITOR=nano ops server edit blog-vps
```

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops server info <name>`

采集服务器的系统、硬件与 Docker 状态

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops server list`

列出所有已配置的服务器

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--filter` | `-f` | (空) | 按 Label（key=val）、Tag 或名称筛选服务器 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops server remove <name>`

别名：`ops server rm`、`ops server delete`

从 servers.yaml 中删除一条服务器条目。

删除条目的同时还会删除 OpsPulse 为该机托管的私钥文件，除非指定 --keep-key，
或仍有其他服务器引用同一把密钥。

仍有备份作业引用该服务器时，会先打印一条警告，并在交互式 shell 中追加一次确认：
该作业会继续使用自己的清单运行到下次执行，因此引用永远不会挡住删除。

示例：
```
  ops server remove old-vps             # 交互确认后删除
  ops server remove old-vps --yes       # 跳过确认提示
  ops server remove old-vps --keep-key  # 保留磁盘上托管的私钥
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--keep-key` | - | `false` | 不删除磁盘上由 OpsPulse 托管的私钥文件 |
| `--yes` | `-y` | `false` | 跳过删除确认提示 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops server set <name>`

增量更新已有服务器条目的指定字段，未指定的字段保持不变。

示例：
```
  ops server set blog-vps --host 203.0.113.10
  ops server set blog-vps --port 2222 --key ~/.ssh/blog_ed25519
  ops server set blog-vps --skip-batch
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--host` | - | (空) | 新的服务器 IP 或主机名 |
| `--key` | `-k` | (空) | 新的私钥路径（留空表示清除） |
| `--no-copy-key` | - | `false` | 私钥位于 ~/.ssh/ 之外时不提示复制到该目录 |
| `--no-skip-batch` | - | `false` | 解除该服务器的 skip-batch 限制 |
| `--port` | `-p` | `0` | 新的 SSH 端口 |
| `--skip-batch` | - | `false` | 将该服务器排除在隐式批量操作之外（如 ops exec -f all、ops doctor） |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops server setup-key <name>`

生成 SSH 密钥对，用已保存的密码把公钥安装到远端主机，并把该服务器绑定到本地私钥。

远端密码不会被修改：这里只是在其之上增加密钥登录，
因此原有密码仍可作为后备方式继续使用。

加上 --remove-password 时，OpsPulse 会先证明新密钥能独立完成认证（刻意不提供密码），
之后才删除 servers.yaml 中的明文密码。这样，实际回退到密码认证的验证
就不可能被误判为密钥可用。

示例：
```
  ops server setup-key web-01                     # 生成并安装专用密钥
  ops server setup-key web-01 --remove-password   # 密钥可用后同时清除明文密码
```

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--remove-password` | - | `false` | 验证密钥可独立认证后，从 servers.yaml 中删除明文密码 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops server test <name>`

测试服务器的 SSH 连通性

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops sftp [server] [flags]`

自动唤起连接到目标服务器的图形化 SFTP 客户端。

支持的 GUI 客户端：
```
  - Windows：WinSCP、Xftp (NetSarang)、FileZilla
  - macOS：  Cyberduck、Transmit、FileZilla
  - Linux：  FileZilla、Nautilus、xdg-open
```

客户端以异步独立进程在后台拉起，终端立即返回可用。

未提供服务器名时，会弹出交互式选择菜单供你挑选。
需要强制使用终端原生 OpenSSH sftp 会话时，传入 --cli。

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--app` | - | (空) | 显式指定 GUI SFTP 客户端名称（winscp、xftp、filezilla、cyberduck）或可执行文件路径 |
| `--cli` | - | `false` | 使用终端原生 OpenSSH sftp 客户端而非 GUI |
| `--list-apps` | - | `false` | 列出本机检测到的 SFTP 客户端 |
| `--path` | - | `/` | 打开的远端初始目录 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops ssh [name] [flags] [-- <ssh_args...>]`

直接打开到指定服务器的原生交互式 SSH 会话。
自动从 servers.yaml 读取连接参数（host、port、user、key_path）。

'--' 之后的参数原样进入 ssh(1) 的选项槽位（-o、-L、-v …）。该槽位必须位于目的地址
之前，也是 ssh(1) 唯一接受选项的位置，因此远程命令无法经 '--' 传递——ssh(1) 会把
命令词当成主机名，ops 在建立连接前就会拒绝这类参数。需要执行命令请改用 --exec。

使用 --exec 时 stdout 只承载命令输出（横幅信息写 stderr），命令的退出码原样成为 ops
的退出码。ops 不注入自己的伪终端：pty 沿用 ssh(1) 的原生规则——stdin 是终端时分配，
因此 tmux/sudo 经 --exec 仍然可用，管道与脚本中得到纯非交互会话；需要强制关闭时追加
"-- -T"。

未提供服务器名时，会弹出交互式菜单供选择要连接的服务器。

| flag | 简写 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--exec` | - | (空) | 以非交互方式在服务器上执行一条命令，并以该命令的退出码退出 |
| `--no-title` | - | `false` | SSH 会话期间不改写终端标题 |

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops template`

列出可用的内置与自定义脚本模板，或查看模板内容。

示例：
```
  ops template list         # 列出全部内置与自定义模板
  ops template show base    # 打印某个模板的内容与元数据
```

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops template list`

列出全部可用模板

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops template show <name>`

查看脚本模板的内容与元数据

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops test <name>`

测试服务器的 SSH 连通性（等价于 ops server test）

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。

### `ops version`

打印版本信息

该命令没有自己的 flag。

继承的持久 flag：`--debug`（定义于 `ops`）。
