# OpsPulse

[![CI](https://github.com/volcano6/opspulse/actions/workflows/ci.yaml/badge.svg)](https://github.com/volcano6/opspulse/actions/workflows/ci.yaml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/volcano6/opspulse)](https://go.dev/)
[![License](https://img.shields.io/github/license/volcano6/opspulse)](LICENSE)

**Infrastructure Action Runner** — Self-hosted personal server automation, backup orchestration, and secure operations.

## Problem

Managing multiple VPS instances means:

- Repeating the same setup commands on every new server
- Scattered backup scripts with no visibility into their status
- No tested restore workflow until disaster strikes
- Secrets spread across `.env` files and memory

OpsPulse solves this by providing a single CLI tool that handles server bootstrap, backup management, disaster recovery, and secret management.

## Status

🚧 **Active Development — Phase 0**

## Quick Start

```bash
# Build from source
make build
./bin/opspulse version

# Or via Docker
docker build -t opspulse .
docker run --rm opspulse version
```

## Roadmap

| Version | Capability |
|---------|------------|
| v0.1 | Server Bootstrap |
| v0.2 | Backup Management |
| v0.3 | Restore & Disaster Recovery |
| v0.4 | Secret Management / 1Password |
| v0.5 | Dashboard (Vue 3) |
| v0.6 | Automation & Notification |
| v1.0 | Complete Platform |

## Security

See [SECURITY.md](SECURITY.md) for our security policy and principles.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and guidelines.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
