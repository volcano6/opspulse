# 告警通知系统指南 (Notification System)

OpsPulse 提供开箱即用的自动化告警分发机制。在备份任务调度执行完毕或失败时，通知子系统会自动解析状态，并通过 Webhook 实时推送到用户配置的通知渠道。

---

## 1. 配置文件 `notifications.yaml`

路径：`$XDG_CONFIG_HOME/opspulse/notifications.yaml`

```yaml
channels:
  # Slack 告警（仅在任务失败时通知）
  - name: slack-ops
    type: webhook
    url: "https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXXXXXXXXXXXXXXXXXX"
    on: failure

  # Discord 运维群（仅在任务失败时通知）
  - name: discord-alerts
    type: webhook
    url: "https://discord.com/api/webhooks/1234567890/abcdefghijklmnopqrstuvwxyz"
    on: failure

  # 飞书 / 钉钉 / 企业微信（支持所有状态）
  - name: feishu-bot
    type: webhook
    url: "https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
    on: always

  # 自定义内部运维监控平台
  - name: internal-webhook
    type: webhook
    url: "https://monitor.example.com/api/v1/opspulse/webhook"
    on: always
```

---

## 2. 字段规范

| 字段 | 类型 | 是否必填 | 说明 |
|:---|:---|:---|:---|
| `name` | 字符串 | **是** | 渠道唯一名称标识（如 `slack-ops`、`discord-alerts`） |
| `type` | 字符串 | **是** | 渠道类型，当前支持 `webhook` |
| `url` | 字符串 | **是** | 接收通知的完整 HTTP / HTTPS 目标地址 |
| `on` | 字符串 | 否 | 触发过滤条件，可选 `failure`（默认）、`success`、`always` |

### 触发条件 (`on`) 行为说明

- `failure`（默认）：仅当备份任务状态为 `failed` 时触发通知。**推荐日常使用，避免正常任务消息刷屏**。
- `success`：仅当备份任务成功完成时触发通知。
- `always`：无论成功还是失败均触发通知。

---

## 3. Webhook 请求 Payload 结构

OpsPulse 发送标准 HTTP POST 请求，Header 包含：
- `Content-Type: application/json; charset=utf-8`
- `User-Agent: OpsPulse-Notifier/1.0`

### JSON 消息体：

```json
{
  "event": "backup_failed",
  "job_name": "web-data",
  "status": "failed",
  "server": "prod-vps",
  "snapshot": "",
  "duration_seconds": 12.45,
  "error": "failed to connect to host: dial tcp 198.51.100.1:22: i/o timeout",
  "timestamp": "2026-09-03T19:30:00Z",
  "text": "[OpsPulse] ❌ Backup job \"web-data\" on \"prod-vps\" FAILED: failed to connect to host (duration: 12.45s)",
  "content": "[OpsPulse] ❌ Backup job \"web-data\" on \"prod-vps\" FAILED: failed to connect to host (duration: 12.45s)"
}
```

> [!TIP]
> 消息体中内置了 `text`（适配 Slack Webhook）和 `content`（适配 Discord Webhook）字段，各主流 IM 平台的 Incoming Webhook 无需额外中间层转发即可直接展示格式化卡片。

---

## 4. CLI 管理与连通性自测

### 查看所有已配置的通知渠道

```bash
opspulse notify list
```

输出示例：
```text
NAME            TYPE       TRIGGER   URL
----            ----       -------   ---
slack-ops       webhook    failure   https://hooks.slack.com/services/...
discord-alerts  webhook    failure   https://discord.com/api/webhooks/...
feishu-bot      webhook    always    https://open.feishu.cn/open-apis/bot/v2/hook/...
```

### 测试通知投递

在正式启用定时备份前，可以通过 `notify test` 验证 Webhook 是否能正常收到测试卡片：

```bash
# 测试指定渠道
opspulse notify test slack-ops

# 测试全部已配置的渠道
opspulse notify test
```
