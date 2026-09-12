# OpsPulse 跨端环境备份与还原教程 (WSL)

OpsPulse v0.8+ 提供了强大的跨端配置流转能力。无论你是从 WSL 备份环境配置并同步到 VPS，还是在新设备上还原开发环境，OpsPulse 都能借助 `op://` 零信任凭证和 `remap` 路径重映射功能，实现无损、安全的自动化流转。

## 场景一：备份 WSL 环境配置

在你的 WSL（例如 Ubuntu）中，你可以创建一个专门用于备份个人配置的 `backups.yaml` 任务。

### 1. 使用 1Password 保护敏感凭据

我们不推荐在 `backups.yaml` 中硬编码 AWS 密钥或 Restic 密码。相反，我们可以使用 1Password 的 `op://` 协议：

```yaml
# ~/.opspulse/backups.yaml
backups:
  - name: my-wsl-env
    server: local  # 声明这是在本地执行的备份
    backend: s3:s3.amazonaws.com/my-bucket/configs
    env:
      # 运行时由 OpsPulse 自动调用 1Password CLI 解析，绝不落盘
      AWS_ACCESS_KEY_ID: "op://Personal/AWS/username"
      AWS_SECRET_ACCESS_KEY: "op://Personal/AWS/credential"
      RESTIC_PASSWORD: "op://Personal/Restic/password"
    paths:
      - ~/.zshrc.local
      - ~/.gitconfig
      - ~/.ssh/config
      - ~/.aws/config
    remap:
      # 为将来的跨机还原做准备：将 /home/vol 映射到 /root
      "/home/vol": "/root"
    retention:
      keep_last: 5
```

### 2. 执行备份

如果系统已配置 1Password CLI，直接运行：

```bash
ops backup run my-wsl-env
```

或者，你也可以使用 1Password 官方推荐的 `op run` 注入方式启动一个持久化的安全 Shell，这能在没有 OpsPulse `op://` 特性支持的旧版本中获得同样的安全性：

```bash
op run --env-file=~/.env.op -- ops backup run my-wsl-env
```

## 场景二：跨机无损还原 (Path Remapping)

当我们在 WSL (`/home/vol/`) 备份的配置需要还原到 VPS (`/root/`) 时，路径通常是不匹配的。如果我们直接还原，会导致在 VPS 上创建不期望的 `/root/home/vol/` 目录结构。

我们在 `backups.yaml` 中配置的 `remap` 此时就会生效：

```yaml
    remap:
      "/home/vol": "/root"
```

当我们在 VPS 或本地通过 `--target-server` 发起还原时，OpsPulse 会自动拦截恢复过程，并在暂存目录将路径转换为目标路径。

```bash
ops restore run my-wsl-env --target-server vps-01
```

在上述命令中：
- `/home/vol/.zshrc.local` 将被准确地还原到 VPS 的 `/root/.zshrc.local`
- 过程完全自动化，无需手动 `mv`。

## 附录：推荐备份的开发环境清单

你可以参考以下清单完善你的 `backups.yaml`：

- Shell: `~/.zshrc`, `~/.bashrc`, `~/.profile`
- Git: `~/.gitconfig`, `~/.gitignore_global`
- SSH: `~/.ssh/config`, `~/.ssh/known_hosts` (注意：私钥不要备份到 S3，交给 OpsPulse 托管到 1Password 即可，见 [1Password 私钥托管指南](../reference/onepassword.md))
- 命令行工具: `~/.aws/config`, `~/.kube/config`, `~/.config/gh/`
- 编辑器: `~/.config/nvim/`
