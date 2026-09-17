# 1Password 私钥托管指南

OpsPulse 可以把 SSH 私钥托管到 1Password，本地只在 `servers.yaml` 里保留一条 `op://` 引用。
连接时按需调用 1Password CLI 取回私钥，用完即弃，私钥不必长期留在磁盘上。

## 前提

需要 1Password CLI（`op`）。**在 WSL 下必须使用 Windows 版 `op.exe`**，这样可以直接复用
1Password 桌面端的解锁状态（Windows Hello / 指纹弹窗）：

1. 在 Windows PowerShell 里安装：`winget install AgileBits.1Password.CLI`
2. 打开 1Password 桌面端 → Settings → Developer → 勾选 **Integrate with 1Password CLI**

> ⚠️ **WSL 里不要用 Linux 版 `op`。** 桌面端集成的通信通道是 Windows 侧的，
> Linux 版 `op` 无论怎么配置都连不上 Windows 桌面端，勾了上面那个选项也没用——
> 它只会报 `No accounts configured for use with 1Password CLI`。
> OpsPulse 在 WSL 下会自动优先选 `op.exe`，连 PATH 上找不到的情况也会去
> `%LOCALAPPDATA%\Microsoft\WinGet\Packages\AgileBits.1Password.CLI_*\op.exe` 兜底探测
> （winget 经常不生成 `Links` 垫片，装了却不在 PATH 上是常见情况）。
>
> 需要强制指定时用环境变量 `OPSPULSE_OP_PATH=/path/to/op.exe`。

如果确实想用 Linux 版 `op`，则必须用 `op account add` 登录，或配置 Service Account Token
（`OP_SERVICE_ACCOUNT_TOKEN`），此时不会有桌面端生物识别解锁。

## 命令

```bash
ops 1p status                      # 先看每台服务器的私钥现在放在哪儿
ops 1p push web vps-01             # 推送指定服务器
ops 1p push --all                  # 推送所有使用本地私钥的服务器
ops 1p push web --delete-local     # 推送成功后顺带删掉本地托管副本
ops 1p pull web                    # 取回本地，写回 ~/.ssh/opspulse_web
ops 1p pull web db-01              # 一次取回多台
ops 1p pull --all                  # 取回全部（脱困通道，见下节）
ops 1p pull --all --yes            # 同上，跳过明文密码确认
ops 1p config                      # 查看当前生效的账号/保险库，并列出可选项
ops 1p config --vault Employee     # 记住默认保险库，以后不用再传 --vault
ops 1p config --unset              # 忘掉记住的默认值
```

### 从 1Password 撤离（脱困通道）

不再续费 1Password、或者要把凭据收回本地时，一条命令就能整体脱离：

```bash
ops 1p pull --all
```

它会把每台服务器托管在 1Password 里的凭据**完整**还原：私钥写回 `~/.ssh/opspulse_<server>`，
密码写回 `servers.yaml`。一个条目同时有私钥和密码时（`ops server setup-key` 会留下这种状态），
两者都会被取回——只取一个会留下一条永远解析不了的 `op://` 引用。

密码只能以明文形式回到 `servers.yaml`，所以只要本次拉取涉及密码，OpsPulse 会先要求确认：

```
⚠️  Warning: pulling will write 2 plaintext password(s) into servers.yaml.
Are you sure you want to proceed? [y/N]:
```

- `--yes`（`-y`）跳过确认，适合脚本；
- 非交互环境（管道、CI）**不给 `--yes` 就直接拒绝退出**，不会挂住，也不会偷偷落盘；
- 结束后会再汇总一次"写入了 N 条明文密码"，提醒你用完删除。

几个细节：

| 参数 | 作用 |
|------|------|
| `--all` | 拉取全部服务器；默认跳过带 `skip_batch` 的服务器并列出它们 |
| `--include-skipped` | 配合 `--all`，把 `skip_batch` 的服务器也一起拉 |
| `--force` | 本地已有**另一把**私钥时强制覆盖 |
| `--yes` / `-y` | 跳过明文密码确认 |

单台失败（1Password 不可达、条目被删）不会中断整批，收尾会打印
`N restored, M skipped, K failed`，只要有失败就以非 0 退出。

> 私钥覆盖判断按**公钥**比对，不是按字节。1Password 取回时会把密钥规范化
> （例如以传统 PEM 上传的 RSA 密钥会以 OpenSSH 格式返回），按字节比会误判成冲突，
> 让人白白去加 `--force`。只有本地那把确实是**另一把**密钥时才会拦下来。

### 不用每次都传 --vault / --account

OpsPulse 只在**真正有歧义**的时候才要求你选。取值优先级从强到弱：

| 优先级 | 保险库 | 账号 |
|--------|--------|------|
| 1 | `--vault` | `--account` |
| 2 | `$OP_VAULT` | `$OP_ACCOUNT` |
| 3 | `ops 1p config --vault` 记住的值 | `ops 1p config --account` 记住的值 |
| 4 | 该账号下**唯一**可访问的保险库（自动选中） | `op` 自己的默认账号 |

所以只有 `Personal` 一个库时，`ops 1p push --all` 直接就能跑，不需要任何参数。
如果账号下有多个库，才会报错并列出候选。

`--vault` / `--account` 一旦显式传过，就会被记住（配置文件
`<配置目录>/onepassword.yaml`，0600）：

```bash
# 这个月用个人账号
ops 1p config --vault Personal --account example.1password.com

# 下个月个人账号到期，一条命令整体切到团队账号
ops 1p config --vault Employee --account acme.1password.com
```

切换后**连接时**解析 `op://` 也会跟着走同一个账号，不用改 shell profile。
如果某次只想临时换一下，直接 `OP_ACCOUNT=xxx ops 1p push --all` 即可——
环境变量优先于记住的值。

> 记住的保险库如果以后被删掉，命令会**告警并自动回退**到剩余可用的库，不会一直卡住；
> 而显式传 `--vault` 传错名字是硬报错。

`push` 会为每台服务器创建一个 **SSH Key** 类型的条目，标题固定为 `opspulse_<服务器名>`。
已经存在同名条目时走更新，不会重复创建。

## 托管之后发生了什么

`servers.yaml` 里的 `key_path` 会从本地路径变成 1Password 引用：

```yaml
servers:
  - name: web
    host: 10.0.0.8
    user: root
    key_path: op://Private/opspulse_web/private key
```

之后所有走 SSH 的功能——`ops ssh`、`ops exec`、`ops server test`、`ops server info`、
`ops doctor`、`ops backup`、`ops cp`——都会在连接时解析这条引用，本地不落盘。

`ops 1p status` 与 `ops server list` 的 AUTH 列都会显示 `1password: <vault>/<item>`，
所以一眼就能看出私钥到底在哪。

## 设计上的几个关键点

- **必须用 JSON 模板写入。** 1Password 的命令行赋值语句
  （`private key=...` 这种 `field=value` 形式）**不支持 SSHKEY 字段类型**，
  而且命令行参数会出现在进程列表中，等于把私钥公开给同机所有进程。
  因此 OpsPulse 采用 `op item template get "SSH Key"` 取官方模板 → 填充 → 通过
  `--template` 交给 CLI，临时文件权限 0600 且用完即删。
- **读取必须带 `?ssh-format=openssh`。** 不加这个查询参数，1Password 返回的是它自己的
  内部存储格式，`ssh` 和 `crypto/ssh` 都解析不了。OpsPulse 会自动补上。
- **WSL 下不走 `cmd.exe` 传参。** 多行私钥经过 Windows 命令解释器会被转义破坏，
  所以 OpsPulse 只用 `cmd.exe` 定位 `op.exe`，实际执行直接调该可执行文件。

## 已知边界

| 场景 | 行为 |
|------|------|
| `ops ssh` / `ops exec` 等 | 私钥在连接时解析，`ops ssh` 会写成 0600 临时文件，会话结束立即删除 |
| GUI SFTP（`ops sftp --app winscp`） | GUI 客户端只认文件，私钥会落到 `~/.ssh/opspulse-1p/<server>`（0600）并保留 |
| `ops export ssh-config` | 系统 `ssh` 读不了 `op://`，因此该主机不会写出 `IdentityFile`，只留注释指引 |
| `ops 1p pull` | 私钥写回 `~/.ssh/opspulse_<server>`（0600）并**保留**；密码以**明文**写回 `servers.yaml`，需确认或 `--yes` |

## 常见问题

**WSL 里报 `No accounts configured for use with 1Password CLI`**
说明当前跑的是 Linux 版 `op`（哪怕桌面端已经勾了「与 1Password CLI 集成」）。
Linux 版连不上 Windows 桌面端，按「前提」装 Windows 版即可；OpsPulse 会自动改用 `op.exe`。
用 `OP_SERVICE_ACCOUNT_TOKEN` 或 `op account add` 也是一种出路，但没有生物识别解锁。

**报 "1Password CLI is installed but not authorised"**
桌面端集成没打开，按上面「前提」的步骤开启；或改用 `op account add` 登录。

**报 "vault \"X\" was not found"**
先跑 `ops 1p config` 看当前账号能访问哪些库。只有一个库时会自动选中；有多个时用
`--vault` 指定，或 `ops 1p config --vault <name>` 记住它。注意 `--vault` 只在该账号范围内查找，
要换账号得配 `--account` 或 `ops 1p config --account <sign-in-address>`。

**报 `authorization timeout`**
这是 1Password 桌面端没能及时收到解锁确认，不是 OpsPulse 的问题——原生 `op vault list`
在同样情况下也会这样报。解锁 1Password 桌面端、确认弹窗后重跑即可。
注意 `op account list` 不需要授权，所以"能列出账号"不代表授权可用。

**想退回本地私钥**
`ops 1p pull <server>` 会把私钥写回 `~/.ssh/opspulse_<server>` 并重新绑定到本地路径，
1Password 里的条目不会被删除。要整体脱离 1Password 用 `ops 1p pull --all`，见
「从 1Password 撤离（脱困通道）」。

**`pull` 说本地有一把不同的密钥**
说明 `~/.ssh/opspulse_<server>` 里已经躺着**另一把**密钥，OpsPulse 不会替你覆盖它。
确认那把密钥不再需要之后再跑 `ops 1p pull <server> --force`。

**`pull` 报 "refusing to write ... without confirmation"**
说明当前是非交互环境，OpsPulse 拒绝在没人确认的情况下把明文密码写进 `servers.yaml`。
确认无误后加 `--yes` 即可。
