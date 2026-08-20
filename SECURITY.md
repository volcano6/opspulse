# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in OpsPulse, please report it responsibly.

Please do NOT open a public GitHub issue for security vulnerabilities.

## Security Principles

- **Secrets are never persisted.** OpsPulse resolves secrets at runtime via SecretResolver (1Password, ENV, etc.) and never writes them to disk or logs.
- **SSH keys are never copied.** OpsPulse uses your local SSH keys via `golang.org/x/crypto/ssh` and never transfers private key material.
- **Logs are local only.** Execution logs are stored locally and never uploaded to any external service.
- **No telemetry.** OpsPulse does not collect or send any usage data.
