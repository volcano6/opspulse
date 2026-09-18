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
ops 1p push --all                  # 推送所有使用本地私钥的服务器（等价于 --filter all）
ops 1p push --filter env=prod      # 按 label / tag / 服务器名筛选
ops 1p push web --delete-local     # 推送成功后顺带删掉本地托管副本
ops 1p pull web                    # 取回本地，写回 ~/.ssh/opspulse_web
ops 1p pull web db-01              # 一次取回多台
ops 1p pull --all                  # 取回全部（脱困通道，见下节）
ops 1p pull --all --yes            # 同上，跳过明文密码确认
ops 1p pull --all --from-vault     # 按条目名从保险库发现凭据，只改绑引用、不落盘
ops 1p config                      # 查看当前生效的账号/保险库，并列出可选项
ops 1p config --vault Employee     # 记住默认保险库，以后不用再传 --vault
ops 1p config --unset              # 忘掉记住的默认值
```

### 选择器：精确名、`--filter`、`--all`

| 写法 | 语义 |
|------|------|
| `ops 1p push web` | 精确服务器名，**不受** `skip_batch` 限制（指名即指令） |
| `ops 1p push --filter env=prod` | 批量筛选（label `key=val`、label 名/值、tag、服务器名），走批量语义 |
| `ops 1p push --all` | 等价于 `--filter all`，走批量语义 |

批量语义 = 先按 filter 筛选，再跳过带 `skip_batch` 的服务器（除非给 `--include-skipped`）。
两种写法**不能混用**：`ops 1p push web --filter env=prod` 会直接报错，`--all` 配一个非 `all`
的 `--filter` 同样报错——静默让其中一个生效，等于targeting了另一组服务器。

`push` 与 `pull` 都遵循同一套规则。

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

### pull 的真实语义

`pull` 不是"取回一份副本"，而是**一次性撤离 / 解除托管**：它会**改写 `servers.yaml`**，
把 `key_path` / `password` 从 `op://` 引用换成具体的本地值。因此：

- **pull 之后不能再 pull**——那些字段已经不是 `op://` 引用了，第二次跑会被跳过。
  想重新托管就再 `ops 1p push` 一次。
- 私钥落盘路径固定是 `~/.ssh/opspulse_<server>`（0600），**不是**当初的原始路径，
  旁边会派生一个 `.pub`。
- 1Password 里的条目**原样保留**，所以这个过程是可逆的、不会误删任何东西。
- 私钥覆盖判断按**公钥**比对，不是按字节。1Password 取回时会把密钥规范化
  （例如以传统 PEM 上传的 RSA 密钥会以 OpenSSH 格式返回），按字节比会误判成冲突，
  让人白白去加 `--force`。只有本地那把确实是**另一把**密钥时才会拦下来。

几个细节：

| 参数 | 作用 |
|------|------|
| `--all` | 拉取全部服务器；默认跳过带 `skip_batch` 的服务器并列出它们 |
| `--filter <k=v>` | 按 label / tag / 服务器名筛选，与 `--all` 同属批量语义 |
| `--include-skipped` | 配合 `--all` / `--filter`，把 `skip_batch` 的服务器也一起拉 |
| `--force` | 本地已有**另一把**私钥时强制覆盖 |
| `--yes` / `-y` | 跳过明文密码确认 |
| `--from-vault` | 不按 `servers.yaml` 的引用找，而是按条目名去保险库发现凭据（见下节） |
| `--materialize` | 配合 `--from-vault`：落盘成完整本地副本；不给则只改绑引用 |
| `--vault <v>` | 配合 `--from-vault`：指定去哪个库找 |

单台失败（1Password 不可达、条目被删）不会中断整批，收尾会打印
`N restored, M adopted, K skipped, B blocked, F failed`，只要有失败或阻塞就以非 0 退出。

### 跨机器取回凭据：`--from-vault`

**`servers.yaml` 是机器本地文件，不会跨机同步。** 所以在 A 机 `push` 之后，B 机上的同名
服务器仍然指向本地 `key_path`——此时在 B 机直接 `ops 1p pull --all` 会得到
`0 restored, N skipped`，全部提示 `no credential is managed in 1Password`。

这不是数据损坏，但"1Password 是中枢、任意机器都能取回"是很自然的预期。因此 `pull` 提供了
`--from-vault`：不去看 `servers.yaml` 里的引用，而是**按 OpsPulse 的确定性条目名**去保险库里找。

```bash
ops 1p pull --all --from-vault               # 只改绑引用，不落盘（推荐）
ops 1p pull --all --from-vault --materialize # 完整落盘（不再续期 1Password 时用）
```

两种模式的区别：

| 模式 | 行为 | 适用场景 |
|------|------|----------|
| **默认（不落盘）** | 把 `key_path` / `password` 写成 `op://` 引用，**不写任何本地文件、不写明文密码**。`ops ssh` 连接时按需 materialize，连接照常可用。 | 日常：1Password 仍是唯一真相源，凭据不散落到磁盘 |
| **`--materialize`** | 私钥写 `~/.ssh/opspulse_<server>`（0600）+ `.pub`，密码以明文写回 `servers.yaml`（触发确认门）。 | 不再续期 1Password，要把凭据完整收回本地 |

> ⚠️ 默认模式会把 `servers.yaml` 从本地路径**改成 `op://` 引用**：如果这台机器的 1Password
> 不可用，它就**连不上了**。要离线也能用，请加 `--materialize`。

两种模式都会先**校验凭据确实可用**（私钥走 `ResolveSSHKey` + 私钥内容校验，密码走非空校验），
全部通过才改写 `servers.yaml`，避免半应用状态把一台本来能连的服务器改坏。

`--from-vault` 只处理**本地已存在的服务器**：保险库里多出来的条目不会自动变成服务器
（条目里没有 host / user，凭空造不出来），结束时只会提示「有 N 个 `opspulse_*` 条目没有
匹配到本地服务器」。

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

`push` 会为每台服务器创建一个 **Login** 类型的条目，标题固定为 `opspulse_<服务器名>_key`，
私钥放在一个自定义的 **CONCEALED** 字段里（字段 id `opspulse_private_key`，label `private key`）。
密码同样是 Login 条目，标题 `opspulse_<服务器名>_password`，用内置 `password` 字段。
已经存在同名条目时走更新，不会重复创建。

**写入后会立刻回读校验**：把引用读回来、算出公钥、与本地私钥比对；为空或不一致就报错，
并且**不改写 `servers.yaml`**。这一步不是多余的——1Password CLI 会接受一次实际什么都没存进去的
写入并返回成功（见下节），没有回读校验的话，一次"成功"的 push 会把一台本来能连的服务器改坏，
而本地路径已经没了。

## 托管之后发生了什么

`servers.yaml` 里的 `key_path` 会从本地路径变成 1Password 引用：

```yaml
servers:
  - name: web
    host: 10.0.0.8
    user: root
    key_path: op://Private/opspulse_web_key/opspulse_private_key
```

之后所有走 SSH 的功能——`ops ssh`、`ops exec`、`ops server test`、`ops server info`、
`ops doctor`、`ops backup`、`ops cp`——都会在连接时解析这条引用，本地不落盘。

`ops 1p status` 与 `ops server list` 的 AUTH 列都会显示 `1password: <vault>/<item>`，
所以一眼就能看出私钥到底在哪。

## 设计上的几个关键点

- **为什么私钥存在 Login 条目里，而不是 "SSH Key" 条目？**
  因为 **1Password CLI 根本写不了 SSH Key 条目**。实测（op 2.34.1）：
  `op item create` 会**接受**带 `private_key` 的 payload、把值回显出来、**退出码 0**，
  然后条目里根本没有这个字段；`op item edit` 则直接拒绝：
  `SSH Key item editing in the CLI is not yet supported`；
  这种空壳条目还会让 `op item get <id> --format json` 整体失败。
  PEM 和 OpenSSH 两种格式都一样，所以不是格式问题，是字段被整体丢弃。
  Login 条目的 CONCEALED 字段是当前**唯一** CLI 可写的私钥载体，PEM / OpenSSH 往返逐字节一致。
  如果 1Password 以后支持编辑 SSH Key 条目，可以平滑迁回。
- **必须用 JSON 模板写入。** 1Password 的命令行赋值语句（`private key=...` 这种
  `field=value` 形式）不支持 SSHKEY 字段类型，而且命令行参数会出现在进程列表中，
  等于把私钥公开给同机所有进程。OpsPulse 取 `op item template get Login` 拿到官方模板，
  填好后通过 **stdin** 交给 CLI（`op` 拒绝 `--template` 与重定向 stdin 同时使用，
  而 Go 起的子进程 stdin 永远是重定向的），私钥因此完全不经过磁盘。
- **`?ssh-format=openssh` 只对真正的 SSHKEY 字段有效。**
  指向真实 SSH Key 条目的 `op://…/private key` 必须带这个查询参数，否则 1Password 返回的是
  它自己的内部存储格式，`ssh` 和 `crypto/ssh` 都解析不了。
  但 OpsPulse 自己的条目用的是**普通文本的 concealed 字段**，对它加这个参数会被 `op` 直接拒绝。
  OpsPulse 靠字段 id 区分这两种情况：`opspulse_private_key` 不加，其他字段照旧自动补上。
- **WSL 下不走 `cmd.exe` 传参。** 多行私钥经过 Windows 命令解释器会被转义破坏，
  所以 OpsPulse 只用 `cmd.exe` 定位 `op.exe`，实际执行直接调该可执行文件。
- **共享私钥按服务器各存一份，不做去重。** 多台服务器用同一把私钥时（很常见，
  例如同一把 GCP 下发的 key），1Password 里就会有多个内容相同的条目——这是**预期行为**，
  不是 bug，不要手动去删。好处是命名完全由服务器名决定（`opspulse_<name>_key`），
  不需要引入"密钥实体"这一层抽象，`~/.ssh/` 下本来就每台一份副本。
  代价是**轮换密钥后要把用到它的服务器都 push 一遍**（`ops 1p push --filter <tag>` 一次搞定）。


## 已知边界

| 场景 | 行为 |
|------|------|
| `ops ssh` / `ops exec` 等 | 私钥在连接时解析，`ops ssh` 会写成 0600 临时文件，会话结束立即删除 |
| GUI SFTP（`ops sftp --app winscp`） | GUI 客户端只认文件，私钥会落到 `~/.ssh/opspulse-1p/<server>`（0600）并保留 |
| `ops export ssh-config` | 系统 `ssh` 读不了 `op://`，因此该主机不会写出 `IdentityFile`，只留注释指引 |
| `ops 1p pull` | 私钥写回 `~/.ssh/opspulse_<server>`（0600）并**保留**；密码以**明文**写回 `servers.yaml`，需确认或 `--yes`。会改写 `servers.yaml` 从而解除托管，pull 之后不能再 pull |
| `ops 1p pull --from-vault` | 默认**不落盘**，只把 `servers.yaml` 改绑为 `op://` 引用；加 `--materialize` 才落盘 |

## 常见问题

**OpsPulse 能用，但 shell 里的 `op item get` 报 `No accounts configured`**
说明 shell 里的 `op` 和 OpsPulse 用的**不是同一个二进制**。WSL 下 PATH 上通常是 Linux 版
`op`（连不上 Windows 桌面端），而 OpsPulse 会自动优先选 `op.exe`。对照方法：

```bash
which -a op op.exe        # shell 实际会跑到哪个
ops 1p config             # OpsPulse 实际驱动哪个，以及鉴权是否通过
```

`ops 1p config` 会打印 OpsPulse 探测到的 CLI 路径、构建类型（Windows / Linux）以及是否处于
WSL，并给出鉴权结论。用 `OPSPULSE_OP_PATH=/path/to/op.exe` 可以强制指定。

**WSL 里报 `No accounts configured for use with 1Password CLI`**
说明当前跑的是 Linux 版 `op`（哪怕桌面端已经勾了「与 1Password CLI 集成」）。
Linux 版连不上 Windows 桌面端，按「前提」装 Windows 版即可；OpsPulse 会自动改用 `op.exe`。
用 `OP_SERVICE_ACCOUNT_TOKEN` 或 `op account add` 也是一种出路，但没有生物识别解锁。

**另一台机器 `ops 1p pull --all` 全是 `no credential is managed in 1Password`**
`servers.yaml` 是机器本地文件，不跨机同步；A 机 push 只会改绑 A 机的配置。
用 `ops 1p pull --all --from-vault` 按条目名从保险库发现凭据，详见
「跨机器取回凭据」。

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
「从 1Password 撤离（脱困通道）」；跨机场景见「跨机器取回凭据」。

**`pull` 说"no credential is managed in 1Password"**
说明这台机器的 `servers.yaml` 里没有 `op://` 引用——凭据可能是在**另一台机器**上 push 的。
用 `ops 1p pull --all --from-vault` 从保险库按条目名取回。

**`push` 报 "could not read it back"**
写入后回读校验没通过（条目为空、读不到、或读回来的公钥与本地私钥不一致）。
`servers.yaml` **没有被改写**，服务器仍然指向原来的本地私钥，可以直接排查：
确认 `ops 1p config` 里鉴权正常，以及该条目确实是 OpsPulse 建的 Login 条目。
如果是迁移前遗留的旧 **SSH Key 空壳条目**（标题 `opspulse_<name>`，没有 `_key` 后缀），
删掉它再 push 即可——OpsPulse 不会再写这种条目。

**`pull` 说本地有一把不同的密钥**
说明 `~/.ssh/opspulse_<server>` 里已经躺着**另一把**密钥，OpsPulse 不会替你覆盖它。
确认那把密钥不再需要之后再跑 `ops 1p pull <server> --force`。

**`pull` 报 "refusing to write ... without confirmation"**
说明当前是非交互环境，OpsPulse 拒绝在没人确认的情况下把明文密码写进 `servers.yaml`。
确认无误后加 `--yes` 即可。
