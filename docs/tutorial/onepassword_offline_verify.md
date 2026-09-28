# 1Password 离线验证教程 (Offline 1Password Verification)

`ops 1p backup` / `ops 1p restore` 走的是 `op` CLI 的桌面端授权，在容器、CI 或没装
1Password 的机器上根本跑不起来。所以仓库自带一个替身：`scripts/opstub` 只回答 OpsPulse
真正用到的那几个子命令，并把每次调用记进日志。用它可以在**完全离线、不需要真实账号**的
情况下跑通「备份 → 换机还原」全链路，并逐条断言。

本页只讲这两条验证路径。凭据在哪儿、保险库里存了什么、合并与冲突规则，见
[1Password 备份与跨机同步指南](../reference/onepassword.md)；命令与 flag 清单见
[命令行参考](../reference/cli.md)。

## 1. 一键跑完 (One-shot)

```bash
make e2e        # 等价于 bash scripts/verify-onepassword.sh，约 10 秒
```

脚本自己构建 `ops` 与替身，用临时目录 + 假 `HOME` 隔离（不碰你的 `~/.ssh`、
`servers.yaml` 与真实 `op`），逐条断言后打印结果：

```text
==> building ops and the op stub
==> a first backup stores the whole inventory in one document
==> assertions
  PASS  servers.yaml is untouched: no op:// reference appears (0)
  PASS  the backup reports every server, credential or not (1)
  ...
==> a repeat backup is three calls: read, edit, read-back
  PASS  the whole backup costs three calls (3)
  ...
OK: all assertions passed
```

最后一行的 `OK: all assertions passed` 才是结论；任何一条断言失败都会以非零码退出。

它钉住的是 **OpsPulse 这一侧的契约**——调用序列与不变量：

- 一次备份把整份 `servers.yaml` 与本机全部私钥写进**一个**以本机名命名的 Secure Note，
  稳定态 **3 次 op 调用**（读旧文档 → `item edit` → 回读校验），而不是每个凭据一次；
- 备份**绝不改写** `servers.yaml`；本机已经读不出来的私钥沿用上一份备份里的副本，并按
  服务器名打印一行说明，而不是悄悄丢掉；
- `servers.yaml` 里残留 `op://` 引用时，备份在**触碰 1Password 之前**就失败退出；
- 回读不是逐字节相同就判定备份失败，并把被替换掉的旧文档写回保险库；
- 还原只读一次备份文档就把私钥写回 `~/.ssh`，**没有**任何逐凭据的 `op read`；
- 无参还原要写明文密码时**先**确认：非交互环境不给 `--yes` 就拒绝，且 `servers.yaml`
  保持逐字节不变；
- 磁盘上已有另一把密钥时拒绝覆盖，直到显式 `--force`；
- 无参还原能在一台空机器上完成起步：先并集合并清单，再写回凭据，且不删除只有本机
  知道的服务器。

## 2. 手工验证 (Manual verification)

想自己看调用序列，就在临时目录里跑一遍——下面每一条都可以直接粘贴：

```bash
demo="$(mktemp -d)"
go build -o "$demo/opstub" ./scripts/opstub     # 替身 op CLI
go build -o "$demo/ops" ./cmd/opspulse          # 真实二进制

export OPSPULSE_HOME="$demo/home"               # 配置与数据都关进临时目录
export OPSPULSE_OP_PATH="$demo/opstub"          # 让 ops 用替身，而不是真实 op
export STUB_OP_NOTE_STORE="$demo/note.store"    # 替身的“保险库”
export STUB_OP_LOG="$demo/op.log"               # 每次 op 调用都记在这里
mkdir -p "$OPSPULSE_HOME"
ssh-keygen -q -t ed25519 -N '' -f "$demo/id_demo"   # 要被备份的私钥

cat > "$OPSPULSE_HOME/servers.yaml" <<YAML
servers:
  - name: web
    host: 203.0.113.10
    user: ubuntu
    key_path: $demo/id_demo
YAML

"$demo/ops" 1p backup --vault Personal
```

期望输出（`<本机名>` 是 `opspulse_inventory_` 后面的主机名）：

```text
💡 Remembered vault "Personal"; future runs use it without --vault.
⬆️  Backing up 1 server(s) and 1 private key(s) into "opspulse_inventory_<本机名>" in vault "Personal"...
🎉 Backed up 1 server(s) to "opspulse_inventory_<本机名>" in vault "Personal" (<N> bytes, verified byte for byte).
   servers.yaml was left unchanged: local disk stays the source of truth.
   Restore on another machine with: ops 1p restore
```

`cat "$demo/op.log"` 会列出这次备份产生的每一次 `op` 调用：读旧文档 → `op item edit`
（条目还不存在，替身照 CLI 的原话报 "could not find item"）→ `op item create` → 回读校验。
条目已存在时不需要 create，也没有任何逐凭据调用——想让替身模拟「条目已存在」，
先 `export STUB_OP_EXISTING=opspulse_inventory_<本机名>` 再跑一次即可。

还原同样离线可验。先把 `key_path` 指到「新机器上还不存在」的位置（模拟换机），并把 `HOME`
也指到临时目录，避免碰到真实 `~/.ssh`：

```bash
cat > "$OPSPULSE_HOME/servers.yaml" <<'YAML'
servers:
  - name: web
    host: 203.0.113.10
    user: ubuntu
    key_path: ~/.ssh/opspulse_web
YAML

export HOME="$demo/fakehome" USERPROFILE="$demo/fakehome"
mkdir -p "$HOME/.ssh"
STUB_OP_EXISTING="opspulse_inventory_<本机名>" "$demo/ops" 1p restore web
```

```text
⬇️  Restoring key for "web" from the 1Password backup document...
✅ "web": key restored to ~/.ssh/opspulse_web.

Restore finished: 1 restored, 0 skipped, 0 blocked, 0 failed.
```

私钥直接从备份文档里取，`op.log` 里只有 3 次调用（列保险库 + 列条目 + 读文档），
**没有**任何逐凭据的 `op read`——这正是「整机只写一个条目」换来的东西。

## 3. 边界与排障 (Limits and troubleshooting)

替身不是 1Password：它只回答 OpsPulse 用到的子命令并记录调用。所以上面两条路径证明的是
**OpsPulse 这一侧的调用序列与不变量**，不证明真实 1Password 会接受这些调用——桌面端授权、
条目类型与字段的真实限制，仍需要在装了 1Password 的机器上验一遍。

`op` 的安装、WSL 下为什么必须用 Windows 版 `op.exe`、还原时的冲突与明文密码确认规则，
见 [1Password 备份与跨机同步指南](../reference/onepassword.md)；只想确认本机链路是否可用，
用只读的 `ops 1p doctor`（`--offline` 完全不联系 1Password）。
