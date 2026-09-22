# 1Password 备份与跨机同步指南

在 OpsPulse 里，**1Password 是备份与跨机器同步的目标，不是运行时依赖**。

凭据平时就放在本地磁盘：`servers.yaml` 里存私钥路径或明文密码，`ops ssh` / `ops exec` /
`ops cp` 直接读取，**不与 1Password 发生任何交互**。这正是这些命令不会弹授权框的原因——
普通连接全程不碰 1Password，哪怕它没解锁、没安装、甚至已经卸载。

1Password 只在两条命令里被触碰：

```bash
ops 1p backup     # 把本机凭据与整份 servers.yaml 上传到 1Password
ops 1p restore    # 把 1Password 里的凭据写回本机磁盘
```

> ⚠️ 如果你在 `servers.yaml` 里看到 `op://` 引用，说明那是旧版本留下的残留。
> 现在的运行时**不会再解析它**，`ops ssh` 会直接报错并让你跑 `ops 1p restore` 迁移到本地凭据。

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

## 命令速览

```bash
ops 1p backup                     # 备份本机全部私钥 + 整份 servers.yaml（一个条目，两次 op 调用）
ops 1p backup --vault Private     # 指定保险库（同时省掉一次列保险库的调用）
ops 1p restore                    # 还原清单与全部凭据（新机器一条命令起步）
ops 1p restore web db-01          # 只还原这几台服务器的凭据（不动 servers.yaml）
ops 1p restore --yes              # 无人值守（跳过明文密码确认）
ops 1p restore --force            # 本地已有另一把私钥时强制覆盖
ops 1p status                     # 看每台服务器的凭据现在放在哪儿（离线）
ops 1p status --remote            # 额外查询保险库里哪些服务器有备份（需授权）
ops 1p config                     # 查看当前生效的账号/保险库，并列出可选项
ops 1p config --vault Employee    # 记住默认保险库，以后不用再传 --vault
ops 1p config --offline           # 只读写本地记忆的默认值，不联系 1Password
ops 1p config --unset             # 忘掉记住的默认值
```

## 备份：`ops 1p backup`

```bash
ops 1p backup
```

它把**整台机器**存进 1Password 的**一个** Secure Note：

1. 整份 `servers.yaml`（含每台服务器的明文密码）；
2. 本机持有的**每一把私钥**的全文。

条目名是 `opspulse_inventory_<hostname>`：`<hostname>` 取 `os.Hostname()` 并清洗
（只保留字母数字与 `-_.`，其余替换成 `_`；取不到主机名则用 `unknown`）。

几个关键性质：

- **两次 op 调用。** 一次写入（`op item edit`，首次改走 `op item create`）+ 一次读回校验。
  旧版是"每台服务器一条私钥条目 + 一条密码条目"，规模稍大就是几十次调用、两分多钟；
  而每次 `op` 调用都是一次桌面端授权往返（Windows 上 `op.exe` **没有缓存**），
  所以**调用次数就是耗时**。整机一个条目把这件事压成常数级。
- **绝不改写 `servers.yaml`。** 本地磁盘始终是唯一真相源，所以备份**不会**改变
  `ops ssh` 的连接方式，也**不会**把一台本来能连的服务器变成依赖 1Password 解锁的服务器。
  这是与旧版 `ops 1p push` 最根本的区别。
- **无条件全量。** 没有服务器选择、没有 `skip_batch` 跳过列表——要么全备份，要么不备份。
  没有凭据的服务器也照常进备份：清单本身就是备份的一部分。
- **不再并发。** 只有一次写入，没有 fan-out，所以 `-p/--parallel` 已移除。
- **`--vault` 能省一次调用。** 没记住保险库、也没给 `--vault` 时，得先 `op vault list`
  才知道往哪儿写；记住了或显式给了就跳过。所以首次 3~4 次调用，之后稳定 2 次。
- **遇到 `op://` 残留直接拒绝。** 如果 `servers.yaml` 里还有服务器持有 `op://` 引用，
  `backup` 会在**触碰 1Password 之前**就报错退出：

  ```
  servers.yaml still holds 'op://' references for 1 server(s): web

  run 'ops 1p restore' to migrate them to local credentials, then back up again
  ```

  否则上传上去的就是一条陈旧的引用，备份本身就不可信了。先 `ops 1p restore` 迁移，再备份。

### 一个条目里装什么

| 内容 | 条目类型 | 标题 | 字段 |
|------|----------|------|------|
| 整机备份 | Secure Note | `opspulse_inventory_<hostname>` | 内置正文 `notesPlain` |

`notesPlain` 里是一段 YAML：

```yaml
version: 1          # 格式版本，留升级余地
machine: laptop     # 机器标识，仅供人读
servers: [...]      # 一份标准的 servers.yaml
keys:               # 服务器名 -> 私钥全文
  web: |
    -----BEGIN OPENSSH PRIVATE KEY-----
    ...
```

`keys` 和 `servers` **并排**，而不是把私钥内联进每台服务器——这样 `servers` 部分始终是一份
标准的 `servers.yaml`，合并、比较、字段级 diff 全部原样复用。

已经存在同名条目时走 `op item edit` **原地更新**，不会重复创建。
注意 `op item create` **不按标题去重**：对已存在的条目调用它会静默产生第二条同名条目。
所以写入顺序是「先 `edit`，只有 CLI 明确回 `could not find item` 才 `create`」。

读不到私钥文件时（路径写错、文件被删）会**告警并跳过那把私钥**，但服务器本身照常进备份。
这正是还原要修的状态，为它放弃整台机器的备份并不划算。

### 备份文档不做合并

旧版的共享条目是**并集**语义，因此 `backup` 要读回来、合并、再写回，还要处理冲突。
现在每台机器各写各的条目，`backup` 是纯粹的**覆盖式写入**：它只反映这台机器的现状。

- `--prefer-local` / `--prefer-remote` 从 `backup` 上**移除**了（它不再合并）；
- 并集与冲突处理全部搬到 `restore`：还原时把所有机器的备份文档并起来，
  才可能遇到"同一个服务器名、两份不同定义"。

公司电脑有 `vps1`、家里电脑有 `vps2`，两边各自 `backup`，各自条目里只有自己那份；
新机器 `restore` 时把两份并起来，得到 `vps1 + vps2`，谁都不会丢。

## 还原：`ops 1p restore`

`restore` 是**唯一的脱困通道**：它把 1Password 里的凭据写回本地磁盘，让整台机器不再依赖
1Password 解锁。

### 无参数：整机还原（新机器起步）

```bash
ops 1p restore
```

顺序是固定的，且很重要：

1. **先还原 `servers.yaml`**（把保险库里所有 `opspulse_inventory_*` 文档并起来，
   再与本机已有的做并集合并）；
2. **再还原每台服务器的凭据**：私钥写到 `~/.ssh/opspulse_<server>`，密码写进 `servers.yaml`。

先清单、后凭据，是因为凭据要靠服务器名去匹配。私钥**直接来自已经读进内存的备份文档**，
不再逐台发起 op 调用。一台全新机器装好 1Password CLI 后，这一条命令就够了。

如果保险库里一个 `opspulse_inventory_*` 文档都没有，不会报错退出，而是提示一句
`No inventory backup in vault ...; restoring credentials for the servers already in servers.yaml.`
然后继续用本机已有的 `servers.yaml` 还原凭据。

### 清单合并是并集，不是覆盖

多个备份文档的合并**只增不减**：

| 情况 | 结果 |
|------|------|
| 只有保险库有 | 加入本地 |
| 只有本机有 | **保留**，绝不删除 |
| 同名、清单字段全等 | 不动 |
| 同名、清单字段有差异 | **冲突**，问你（见下） |
| 同名、只有凭据字段不同 | 不算冲突：凭据字段不参与冲突判定，本机已有的值保留 |

凭据字段（`key_path` / `password`）单独走一套规则，且**永远不会**构成冲突：它们天然是
机器本地的——这台机器存一个密钥文件路径，那台机器存一个明文密码——按"值不同"判冲突会
每次还原都吵。规则是：

- 只有一边有值 → 用有值的那边；
- 两边都有值且不同 → **保留本机的**，不来回 churn；
- 两边都没有 → 都不写。

注意即使清单字段冲突、你选了 `--prefer-remote`（主机定义听备份的），凭据字段**仍然保留
本机的**：主机怎么连是一回事，本机用哪把密钥是另一回事。

### 冲突：交互问，非交互拒绝

交互环境下逐台打印字段级差异，再问一次：

```
⚠️  box (host: 10.0.0.1 -> 10.0.0.9)
   [l]ocal / [r]emote / [a]ll-remote / [A]ll-local / [q]uit:
```

`a` / `A` 是"剩下的全按这个来"，`q` 直接放弃（**什么都不写**）。

非交互环境（管道、CI）**不会挂住**：直接报错并列出全部冲突，要求你显式表态：

```bash
ops 1p restore --prefer-local    # 本机赢
ops 1p restore --prefer-remote   # 备份赢
```

`--prefer-local` / `--prefer-remote` 是**显式选择**，没有"默认本地赢"这回事——
静默替你选一个，等于谎称另一个选项不存在。两者不能同时给。

### 具名参数：只还原这几台的凭据

```bash
ops 1p restore web db-01
```

只还原指定服务器的凭据，**不动 `servers.yaml`**。名字不存在于 `servers.yaml` 时是**报错**，
而不是静默跳过——最常见的成因就是"清单还没还原就先还原凭据"，报错会直接告诉你先跑
无参数的 `ops 1p restore`。

凭据同样优先从备份文档里取（读一次文档，所有点名服务器的私钥都在里面）；
只有文档里没有某台服务器的私钥时，才回退去按条目名找旧格式的条目。

### 明文密码确认

密码只能以明文形式回到 `servers.yaml`，所以只要本次还原会写入密码，OpsPulse 会先要求确认：

```
⚠️  Warning: restoring will write 2 plaintext password(s) into servers.yaml.
Are you sure you want to proceed? [y/N]:
```

- `--yes`（`-y`）跳过确认，适合脚本；
- 非交互环境（管道、CI）**不给 `--yes` 就直接拒绝退出**，不会挂住，也不会偷偷落盘。

确认发生在**批量开始之前**，所以拒绝不会留下一个"改了一半"的状态。

### 私钥覆盖判断按公钥比对

本地 `~/.ssh/opspulse_<server>` 已经躺着一把**不同的**密钥时，OpsPulse 不会替你覆盖，
需要 `--force`。

比对的是**公钥**而不是字节。1Password 取回时会把密钥规范化（例如以传统 PEM 上传的 RSA
密钥会以 OpenSSH 格式返回），按字节比会把同一把密钥误判成冲突，让人白白去加 `--force`。

### 遗留 `op://` 迁移（临时兼容路径）

如果 `servers.yaml` 里某台服务器的 `key_path` / `password` 还是 `op://` 引用，
`restore` 会把它当作"迁移"处理：

- 引用按**原样**使用（它精确指向旧版推送时用的那个条目，按条目名反查反而可能漏掉非标准命名）；
- 迁移完成后打印一行 `⚠️  Migrated N server(s) from 'op://' references to local credentials.`，
  并顺手清理旧版本遗留在 `~/.ssh/opspulse-1p` 的临时私钥副本；
- 如果 `ops` 守护进程（daemon）正在运行，会提示你重启它以加载新配置。

> 这条兼容路径是临时的，会在未来的版本里移除。日常 `restore` 若没有迁移发生，
> 就不会去碰那个历史临时目录——清理是**全量**的，绝不能误触发。

### 结果汇总

每台服务器都会打印一行明细，收尾再给一行汇总：

```
Restore finished: N restored, M skipped, K blocked, B failed.
```

- `restored`：确实有凭据落回本地；
- `skipped`：本机已有可用凭据，无需改动；
- `blocked`：需要人工决策才能继续（例如本地已有另一把密钥、又没给 `--force`）；
- `failed`：1Password 不可达、条目被删等硬失败。

只要有 `failed` 或 `blocked`，命令就以非 0 退出。单台失败不会中断整批。

`restore` 结束后还会检查保险库里有没有 `opspulse_*` 条目**没有**匹配到本机的任何服务器，
列出提示。备份文档（`opspulse_inventory_*`）不算孤儿：它是整机备份的载体，本来就不对应
任何单台服务器。它**不会**据此凭空创建服务器——条目里没有 host / user / port / tags，
造不出一台可用的服务器。

### 怎么删除一台服务器（重要）

因为是并集，**备份里的服务器只能手工删**。顺序不能反：

1. 在**还持有它**的每台机器上 `ops server remove <name>`，然后各跑一次 `ops 1p backup`
   （`backup` 是覆盖式写入，这一跑就把它从**这台机器**的条目里删掉了）；
2. 剩下的机器重复第 1 步；任何一台漏掉，它的条目里就还留着这台服务器。

> ⚠️ 只在一台机器上删没用：只要还有任何一台机器的条目（或 `servers.yaml`）留着它，
> 下一次在**那台机器**上 `restore` 就会把它加回来。

## `ops 1p status`：凭据现在放在哪儿

```bash
ops 1p status
ops 1p status --filter env=prod
ops 1p status --remote
```

**默认离线**：只读 `servers.yaml`，因此**从不联系 1Password、从不弹授权框**。
查看"哪些服务器还需要迁移"这件事本身，不应该要求先解锁 1Password。

输出是一个表：

```
NAME     KEY                          PASSWORD
web      local file (~/.ssh/id_ed25519, managed)   -
legacy   legacy 1password ref (Private/opspulse_legacy_key)   -
```

残留的 `op://` 引用会明确标注为 `legacy 1password ref (...)`，而不是"1password"——
运行时已经拒绝它，它是一个**待修复的问题**，不是一种可用的配置。

加 `--remote` 会额外查询保险库，把表换成 `LOCAL KEY` / `1P BACKUP` 两列，
告诉你哪些服务器在 1Password 里有备份（这一步需要授权）。它会读一遍各机器的备份文档，
所以新格式下这个列会显示文档的条目名（`opspulse_inventory_<hostname>`）——
服务器现在住在文档里，旧格式的 `opspulse_<server>_key` 条目只是历史遗留。

`status` 还会检查每个 `local file` 指向的密钥文件**是否真的在这台机器上**。缺失就点名
告警，并**不再**打印"全部本地"的确认——否则那句绿字会被第一次连接失败当场推翻。
（重装、改名、或者 `~/.ssh` 里一个手滑的 `rm` 都会造成这种状态。）

如果 `~/.ssh/opspulse-1p` 里还有旧版本遗留的临时私钥，`status` 会列出来。这些是
`op://` 时代的残留，`ops 1p restore` 会自动清理。`ops sftp --cleanup` 已废弃：
仍然可用，但已从帮助里隐藏，将来会移除。

## `ops 1p config`：记住默认的保险库与账号

OpsPulse 只在**真正有歧义**的时候才要求你选。取值优先级从强到弱：

| 优先级 | 保险库 | 账号 |
|--------|--------|------|
| 1 | `--vault` | `--account` |
| 2 | `$OP_VAULT` | `$OP_ACCOUNT` |
| 3 | `ops 1p config --vault` 记住的值 | `ops 1p config --account` 记住的值 |
| 4 | 该账号下**唯一**可访问的保险库（自动选中） | `op` 自己的默认账号 |

所以只有 `Personal` 一个库时，`ops 1p backup` 直接就能跑，不需要任何参数。
如果账号下有多个库，才会报错并列出候选。

`--vault` / `--account` 一旦显式传过，就会被记住（配置文件
`<配置目录>/onepassword.yaml`，0600）：

```bash
ops 1p config --vault Personal --account example.1password.com
ops 1p config --vault Employee --account acme.1password.com
```

`ops 1p config --offline` 只读写本地记忆的默认值，**完全不联系 1Password CLI**——
在保险库暂时解不开锁、但又想查看或修改记住的默认值时用它。

> 记住的保险库如果以后被删掉，命令会**告警并自动回退**到剩余可用的库，不会一直卡住；
> 而显式传 `--vault` 传错名字是硬报错。

## 已退役的 `push` / `pull`

旧版的 `ops 1p push` 与 `ops 1p pull` 已经**退役**。它们是隐藏命令，运行只会得到明确的
迁移指引：

```
$ ops 1p push --all
Error: 'ops 1p push' has been retired; use 'ops 1p backup' instead

$ ops 1p pull --all --materialize
Error: 'ops 1p pull' has been retired; use 'ops 1p restore' instead
```

它们的旧参数（如 `--materialize`、`--delete-local`）仍然被注册为**隐藏参数**，
所以 `ops 1p push --materialize` 会得到上面的重命名提示，而不是 Cobra 的
`unknown flag` 报错。

为什么不能简单地把 `push` 当成 `backup` 的别名？因为 `push` 的语义是**改写 `servers.yaml`
为 `op://` 引用**，而 `backup` 恰恰相反——它绝不改写 `servers.yaml`。两者不是同一个动作，
悄悄转发等于在你背后改变了本机配置。

## 仍然保留：`backups.yaml` 里的 `env: op://`

一个**独立**的运行时特性：`backups.yaml` 的备份任务 `env:` 字段里可以使用 `op://` 协议，
运行时按需调用 1Password 解析并注入环境变量（不落盘）：

```yaml
jobs:
  - name: nightly-db-dump
    server: db-01
    env:
      MYSQL_PWD: op://Private/mysql-prod/password
```

这与 SSH 凭据是两码事：SSH 凭据必须本地化（否则每次连接都弹授权框），
而备份任务里的 `op://` 只在**该任务执行的那一刻**解析一次，属于可接受的运行时开销。
`ops backup run` / `ops restore run` 都会经过同一条解析路径。

## 设计上的几个关键点

- **为什么整机备份放在 Secure Note 的正文里，而不是每台服务器一条目？**
  因为 **1Password CLI 根本写不了 SSH Key 条目**。实测（op 2.34.1）：
  `op item create` 会**接受**带 `private_key` 的 payload、把值回显出来、**退出码 0**，
  然后条目里根本没有这个字段；`op item edit` 则直接拒绝：
  `SSH Key item editing in the CLI is not yet supported`；
  这种空壳条目还会让 `op item get <id> --format json` 整体失败。
  PEM 和 OpenSSH 两种格式都一样，所以不是格式问题，是字段被整体丢弃。
  旧版因此把私钥塞进 Login 条目的 CONCEALED 字段——能写，但要**每台一条**。
  现在私钥是 Secure Note 正文里的一段文本（`notesPlain`，op 2.34.1 上多行 YAML
  写入/回读逐字节一致，含"末尾无换行"的情形），整台机器只需要**一条**。
  旧版按服务器命名的 Login 条目仍能**读**，是 `restore` 的回退路径。
- **`?ssh-format=openssh` 只对真正的 SSHKEY 字段有效。**
  指向真实 SSH Key 条目的 `op://…/private key` 必须带这个查询参数，否则 1Password 返回的是
  它自己的内部存储格式，`ssh` 和 `crypto/ssh` 都解析不了。
  而 OpsPulse 旧版条目用的是**普通文本的 concealed 字段**，对它加这个参数会被 `op` 直接拒绝。
  OpsPulse 靠字段 id 区分这两种情况：`opspulse_private_key` 不加，其他字段照旧自动补上。
- **写入文档是本地拼的，不取模板。** 旧版要先 `op item template get Login` 拿官方模板再填，
  那是一次额外的往返。Secure Note 的文档结构固定（一个 `notesPlain` 字段），
  本地拼好直接走 **stdin** 交给 CLI 即可——私钥因此完全不经过磁盘，
  也完全不经过命令行参数（命令行参数会出现在进程列表里）。
  代价是如果 1Password 改了 Secure Note 的字段定义，得跟着改 `secret.BuildInventoryItem`。
- **先 `edit`，只在「找不到条目」时才 `create`。** `op item create` **不按标题去重**：
  对一个已存在的条目调用它会静默产生**第二条同名条目**。所以写入顺序必须是
  「先 `op item edit <title>`（标题直接定位条目，省掉 `op item list`），
  只有 CLI 明确回 `could not find item` 才 `create`」。
  判断依据是**错误文本**而不是退出码——`op` 所有失败都返回 1。
- **写入后回读校验。** 1Password CLI 接受过一次实际什么都没存进去的写入并返回成功
  （见上），所以 `backup` 写完会立刻把内容读回来**逐字节**比对；不一致就报错，
  **不会留下一个"看起来成功"的备份**。一个悄悄出错的备份比没有备份更糟——
  你只会在新机器上才发现，而那时已经无从回退。
- **WSL 下不走 `cmd.exe` 传参。** 多行私钥经过 Windows 命令解释器会被转义破坏，
  所以 OpsPulse 只用 `cmd.exe` 定位 `op.exe`，实际执行直接调该可执行文件。
- **共享私钥不去重。** 多台服务器用同一把私钥时（很常见，例如同一把 GCP 下发的 key），
  文档的 `keys` 里会有多个内容相同的值——这是**预期行为**，不是 bug。
  好处是 `keys` 的键就是服务器名，不需要引入"密钥实体"这一层抽象，
  `~/.ssh/` 下本来就每台一份副本。

## 已知边界

| 场景 | 行为 |
|------|------|
| `ops ssh` / `ops exec` / `ops cp` | **完全不碰 1Password**，直接读本地 `servers.yaml`；残留的 `op://` 引用会快速失败并指向 `ops 1p restore` |
| `ops 1p backup` | 把整份 `servers.yaml` + 本机全部私钥写进一个 `opspulse_inventory_<hostname>` 条目；**不改写 `servers.yaml`**；有 `op://` 残留时先报错拒绝 |
| `ops 1p restore`（无参数） | 先还原清单（并集合并所有机器的备份文档）、再还原全部凭据；密码以**明文**写回 `servers.yaml`（需确认或 `--yes`） |
| `ops 1p restore <name>...` | 只还原指定服务器的凭据，不动 `servers.yaml`；名字不存在则报错 |
| `ops 1p restore` 遇到遗留 `op://` | 原样使用该引用并迁移为本地凭据，顺带清理 `~/.ssh/opspulse-1p`；提示重启 daemon |
| `ops 1p status` | **离线**，只读 `servers.yaml`，从不弹授权框 |
| `ops 1p status --remote` | 额外查询保险库并读取各机器的备份文档，需授权 |
| `ops 1p config --offline` | 只读写本地记忆的默认值，不联系 CLI |
| `ops 1p push` / `ops 1p pull` | 已退役（隐藏命令），运行只给出重命名指引 |
| `ops export ssh-config` | 系统 `ssh` 读不了 `op://`；若某主机仍是引用，则不写 `IdentityFile`，只留注释指引 |
| `backups.yaml` 的 `env: op://` | **仍然支持**：任务执行时按需解析注入，属独立的运行时特性 |

## 常见问题

**`ops ssh` 报 `uses an 'op://' ... which is no longer supported at runtime`**
说明 `servers.yaml` 里还有旧版本留下的 `op://` 引用。跑一次 `ops 1p restore`
把它迁移成本地凭据即可（引用会原样从 1Password 取回并落盘）。

**`ops 1p backup` 报 `servers.yaml still holds 'op://' references for N server(s)`**
同上——备份会拒绝上传陈旧的引用。先 `ops 1p restore` 迁移，再 `ops 1p backup`。

**全新机器上连 `servers.yaml` 都没有，怎么起步？**
装好 1Password CLI 后直接 `ops 1p restore`：它会先还原整份清单，再还原全部凭据。
前提是**已经有一台机器跑过 `ops 1p backup`**，否则它会提示没有清单备份、并改用本机
（空的）`servers.yaml` 继续。详见「还原：`ops 1p restore`」。

**`ops 1p restore` 报 "no inventory backup in vault ..."**
这不是错误，只是一句提示：保险库里还没有任何 `opspulse_inventory_*` 文档，于是它退回到
"用本机已有的 `servers.yaml` 还原凭据"。到一台已经有 `servers.yaml` 的机器上先跑
`ops 1p backup` 即可建立备份文档。

**`ops 1p backup` 报 "did not store the backup verbatim"**
写入后回读校验没通过：1Password 存进去的正文与刚写的不是同一份。
这是 CLI 静默丢字段那一类故障的兜底（见「设计上的几个关键点」），
此时备份**不可信**，命令以非 0 退出。排查：`ops 1p config` 看鉴权，
确认 `opspulse_inventory_<hostname>` 确实是 OpsPulse 建的 Secure Note 条目；
必要时删掉它重跑（下一次会走 `create` 重建）。

**换过主机名，保险库里出现了两个 `opspulse_inventory_*` 条目**
条目名里的机器名就是主机名。主机名变了，OpsPulse 就认不出旧条目了，于是新建一个。
这不会丢数据：`restore` 会把**所有**备份文档并起来。不想要旧条目的话，
`restore` 之后手工在 1Password 里删掉它即可。

**删了一台服务器，下次还原又回来了**
这是并集语义的预期结果。正确顺序是先在**还持有它**的每台机器上
`ops server remove <name>` 并各跑一次 `ops 1p backup`。详见「怎么删除一台服务器」。

**`ops 1p restore` 之后某台服务器连不上，说密钥文件不存在**
如果它不在本次还原的目标里（具名还原只处理你点名的服务器），就不会落盘。
跑一次无参数的 `ops 1p restore` 即可覆盖全部服务器。

**`--prefer-local` 报 "contradict each other"**
这两个参数是互斥的显式选择，同时给会被拒绝：

```
Error: --prefer-local and --prefer-remote contradict each other; pick one
```

只给一个，或者两个都不给（交互环境下会逐台问你）。

**OpsPulse 能用，但 shell 里的 `op item get` 报 `No accounts configured`**
说明 shell 里的 `op` 和 OpsPulse 用的**不是同一个二进制**。WSL 下 PATH 上通常是 Linux 版
`op`（连不上 Windows 桌面端），而 OpsPulse 会自动优先选 `op.exe`。对照方法：

```bash
which -a op op.exe        # shell 实际会跑到哪个
ops 1p config             # OpsPulse 实际驱动哪个，以及鉴权是否通过
```

`ops 1p config` 会打印 OpsPulse 探测到的 CLI 路径、构建类型（Windows / Linux）以及是否处于
WSL，并给出鉴权结论。用 `OPSPULSE_OP_PATH=/path/to/op.exe` 可以强制指定。

**报 "1Password CLI is installed but not authorised"**
桌面端集成没打开，按上面「前提」的步骤开启；或改用 `op account add` 登录。
这句提示只在 CLI 确实报出登录/鉴权类错误（`not signed in`、`No accounts configured`
等）时才会出现。如果 CLI 报的是别的原因，看到的是下面那条。

**报 "The 1Password CLI did not complete this call"**
这条提示的意思是：CLI 这次没跑完，而它给出的原因**不指向登录问题**，所以去检查登录设置没用。

最常见的成因是 1Password 桌面端弹了授权框却没人点：`op` 会一直等桌面端应答，等到超时为止。
WSL 下这个等待超时就是 Windows/WSL 中继放弃，报 `UtilAcceptVsock: accept4 failed 110`
（被 `timeout` 杀掉时则是 `exit status 124`）。中继这条消息有时落在 stderr 上，有时干脆不落，
所以提示里不假定它一定说了什么。处理办法：打开 1Password 桌面端、把待处理的弹窗点掉、
确认已解锁，再重跑。

OpsPulse 特意区分这两类失败：只有 stderr 真的出现登录/鉴权字样时才提示去开桌面端集成。
把"人离开了、没人点弹窗"误报成"去改集成开关"，会让人去修一个本来就没坏的东西。

**报 "vault \"X\" was not found"**
先跑 `ops 1p config` 看当前账号能访问哪些库。只有一个库时会自动选中；有多个时用
`--vault` 指定，或 `ops 1p config --vault <name>` 记住它。注意 `--vault` 只在该账号范围内查找，
要换账号得配 `--account` 或 `ops 1p config --account <sign-in-address>`。

**报 `authorization timeout`**
这是 1Password 桌面端没能及时收到解锁确认，不是 OpsPulse 的问题——原生 `op vault list`
在同样情况下也会这样报。解锁 1Password 桌面端、确认弹窗后重跑即可。
注意 `op account list` 不需要授权，所以"能列出账号"不代表授权可用。

**`ops 1p restore` 说本地有一把不同的密钥**
说明 `~/.ssh/opspulse_<server>` 里已经躺着**另一把**密钥，OpsPulse 不会替你覆盖它。
确认那把密钥不再需要之后再跑 `ops 1p restore <server> --force`。

**`ops 1p restore` 报 "refusing to write ... without confirmation"**
说明当前是非交互环境，OpsPulse 拒绝在没人确认的情况下把明文密码写进 `servers.yaml`。
确认无误后加 `--yes` 即可。
