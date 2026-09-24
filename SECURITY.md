# 安全策略与规范

## 🛡️ 漏洞报告

如果你在 OpsPulse 中发现了任何安全漏洞，请通过负责任的方式向维护者报告。

请**不要**在 GitHub 上公开提交包含漏洞细节的公共 Issue：公共 Issue 里的内容在修复前
一直是公开可读的，任何人都能照着复现。

### 私密上报入口（GitHub Private Security Advisory）

OpsPulse 使用 GitHub 的私密漏洞报告（Private Vulnerability Reporting）作为唯一渠道：

- 直接打开 <https://github.com/volcano6/opspulse/security/advisories/new>
- 或从仓库页面进入：**Security → Advisories → Report a vulnerability**（需先登录 GitHub）

草稿只对仓库维护者可见，你可以在这个线程里跟进、协商披露时间。修复发布后我们会在此
advisory 中致谢（除非你要求匿名）。

如果上面这个入口因为网络等原因打不开，请**只**开一个不含任何细节的公开 Issue，
说明「需要一个私密渠道」，我们会回应可用的联系方式——不要在公开 Issue 里贴任何证据。

### 报告里请包含

- 版本（`ops version` 的输出）、操作系统与 Go 版本；
- 最小复现步骤，或一条能被验证的触发路径；
- 影响判断：能读到什么、能写什么、是否需要攻击者已经能在本机执行代码。

**请先脱敏**：把真实主机名、内网地址、用户名、`servers.yaml` 片段、私钥、`op://`
引用、webhook URL（其中通常带 token）替换成占位值再贴进来——我们只需要形状，
不需要你的真实环境。

### 支持版本范围

| 版本 | 支持状态 |
|------|----------|
| `0.4.x` | ✅ 接受漏洞报告并发布修复 |
| `0.3.x` 及更早 | ❌ 不再维护；请先升级到受支持版本（修复不会单独回移到旧版） |

我们通常在 3 个工作日内确认收到报告，确认影响后给出修复版本与披露计划。

---

## 🔒 核心安全原则

- **敏感凭据安全管理 (Secure secret handling)**：OpsPulse 严格通过环境变量或受控配置注入敏感凭据（如 RESTIC_PASSWORD、对象存储密钥等），绝不将明文密码硬编码，日志与远端脚本中会对敏感信息进行脱敏与隔离，绝不向第三方上传。
- **SSH 密钥绝不外传 (SSH keys are never copied)**：OpsPulse 基于本地 `golang.org/x/crypto/ssh` 直接发起认证，绝不会将你的私钥文件拷贝到远端或上传第三方。
- **日志仅保存在本地 (Logs are local only)**：所有执行日志仅落盘在用户本机的 `$XDG_DATA_HOME/opspulse/logs/` 目录下，绝不向任何外部服务上传。
- **零遥测与追踪 (No telemetry)**：OpsPulse 不收集、不存储、不发送任何用户使用数据或行为遥测。

---

## 🔑 凭据与密钥的处理方式（与上面四条原则的对应关系）

上面四条是原则，这里写清它们各自落在代码里的哪条路径上，方便你在评审或审计时逐条核对：

- **凭据默认只存在本地**（对应「敏感凭据安全管理」）：SSH 私钥路径与明文密码字段位于
  `servers.yaml`（权限 `0600`）；`ops ssh` / `ops exec` / `ops cp` 直接读本地凭据，全程
  不与 1Password 交互，因此不会弹授权框。`backups.yaml` 的 `env:` 默认也是明文（权限 `0600`）；
  不想落盘时改用 `op://` 引用，由执行时按需解析、不写回文件。安装密钥后可执行
  `ops server setup-key <name> --remove-password`，在验证公钥可用后清除本地明文密码。
- **1Password 是备份目标，不是运行时依赖**（对应「SSH 密钥绝不外传」的边界）：
  `ops 1p backup` 把本机私钥与整份 `servers.yaml` 写进**你自己的** 1Password 保险库中的
  一个 Secure Note，用于换机与灾备；它**绝不改写** `servers.yaml`，写入后回读逐字节校验。
  `ops 1p restore` 是唯一把凭据写回磁盘的路径，写回明文密码前需要显式确认。
  1Password CLI 的使用与账号权限由你自己掌握，OpsPulse 不接触你的 1Password 主密码。
- **日志与远端执行的脱敏**（对应「日志仅保存在本地」）：执行日志只落在本机
  `$XDG_DATA_HOME/opspulse/logs/`；远端脚本经 stdin 下发，凭据不进入命令行参数或进程列表。
  一个例外要注意：`ops notify list` 会**完整**打印已配置的 webhook URL（其中通常含 token），
  请勿把它的输出重定向到共享文件或粘贴进 Issue。
- **零遥测**（对应「零遥测与追踪」）：除你自己配置的 webhook（告警）与 1Password CLI
  （备份/还原）之外，OpsPulse 不向任何第三方发起网络请求。

发现与上述描述不符的行为，按「漏洞报告」一节私密上报即可——**「实现与文档不符」同样算**。
