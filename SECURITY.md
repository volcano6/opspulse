# 安全策略与规范

## 🛡️ 漏洞报告

如果你在 OpsPulse 中发现了任何安全漏洞，请通过负责任的方式向维护者报告。

请**不要**在 GitHub 上公开提交包含漏洞细节的公共 Issue。

---

## 🔒 核心安全原则

- **敏感凭据绝不落盘 (Secrets are never persisted)**：OpsPulse 在运行时通过 SecretResolver（如 1Password、系统环境变量等）动态解析敏感凭据，绝不将明文密码或 Token 写入磁盘文件或日志。
- **SSH 密钥绝不外传 (SSH keys are never copied)**：OpsPulse 基于本地 `golang.org/x/crypto/ssh` 直接发起认证，绝不会将你的私钥文件拷贝到远端或上传第三方。
- **日志仅保存在本地 (Logs are local only)**：所有执行日志仅落盘在用户本机的 `$XDG_DATA_HOME/opspulse/logs/` 目录下，绝不向任何外部服务上传。
- **零遥测与追踪 (No telemetry)**：OpsPulse 不收集、不存储、不发送任何用户使用数据或行为遥测。
