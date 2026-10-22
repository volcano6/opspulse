# OpsPulse

[![CI](https://github.com/volcano6/opspulse/actions/workflows/ci.yaml/badge.svg)](https://github.com/volcano6/opspulse/actions/workflows/ci.yaml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/volcano6/opspulse)](https://go.dev/)
[![License](https://img.shields.io/github/license/volcano6/opspulse)](LICENSE)

English | [简体中文](README_zh.md)

**Infrastructure Action Runner** — A lightweight, single-binary CLI for self-hosted server management, environment bootstrapping, and structured restic backups.

> **Target Audience**: Single operators and developers managing 10–20 VPS instances from local WSL/Linux environments.
> **Explicit Non-Goals**: No Web UI, no enterprise PaaS bloat, no multi-tenant RBAC, and no Kubernetes/Swarm orchestration.
> **Philosophy**: Local-first, zero remote agent, and fail-closed security.

---

## Why OpsPulse?

When managing multiple cloud VPS instances (Oracle Cloud, RackNerd, BandwagonHost, LightNode, etc.), solo developers repeatedly face the same operational overhead:

- **Repetitive Host Setup**: Re-running baseline system packages, user creation, and security hardening on every fresh VPS.
- **Scattered Backups**: Disjointed scripts and crontabs spread across servers without centralized status visibility or verified recovery drills.
- **Credential Sprawl**: SSH keys and tokens scattered across plaintext configuration files with no secure, reproducible way to restore them on a fresh workstation.

OpsPulse consolidates these responsibilities into a single local binary with no remote agent. Managed hosts still need the standard tools used by the selected workflow, such as `bash`, `restic`, Docker/Compose, and the supported database dump utilities.

---

## Core Capabilities (Working Today)

### 1. Server Inventory & Agentless Direct Connect
- **Declarative Inventory**: Manage hosts, SSH credentials, jump hosts (bastions), and tags in a clean YAML format (`servers.yaml`).
- **One-Shot Hardware & Docker Probing**: `ops server info <name>` executes an agentless probe via a single SSH round-trip, returning formatted OS, hardware specs, and Docker runtime summaries.
- **Native Interactive SSH**: `ops ssh <name>` opens your native terminal session directly using inventory metadata—no need to remember IP addresses, non-standard ports, or key locations.

### 2. Shell Template Provisioning (Bootstrap)
- **19 Built-in Templates**: Provision fresh machines using battle-tested shell templates with YAML frontmatter (`base`, `docker`, `security`, `restic`, etc.).
- **Local Overrides**: Override any official template by placing a matching script in your configuration directory.

### 3. Structured restic Backup & Precision Restore
- **Declarative restic Orchestration**: Define backup jobs in `backups.yaml`. Features tunable concurrency, dry-run previews, automated retention policies, and structured SQLite execution logging.
- **Disaster-Ready Emergency Restore (`--allow-untracked`)**: Defaults to fail-closed audit protection. In disaster scenarios where local SQLite is inaccessible or locked, `--allow-untracked` permits emergency recovery to proceed while flagging the run with exit code `3`.
- **Targeted Asset Model**: Individual services, volumes, and database dumps can be tracked as discrete assets in `assets.yaml` for granular, single-asset restoration.

### 4. Local-First Credentials & 1Password Disaster Recovery
- **Zero Third-Party Dependency in Hot Path**: Daily operations (`ops ssh`, `ops exec`, `ops cp`) read private keys directly from your local filesystem with strict `0600` permissions.
- **One-Command Workstation Disaster Recovery**: `ops 1p backup` packages your entire inventory and managed private keys into an encrypted 1Password Secure Note. Setting up a fresh machine takes minutes with `ops 1p restore`.

---

## Quick Start

```bash
# 1. Add a server (interactive host key trust, silent password input, optional key injection)
ops add vps-01 203.0.113.10 --tags prod

# 2. Inspect host and Docker runtime status
ops server info vps-01

# 3. Connect via native SSH
ops ssh vps-01

# 4. Run a declarative backup job
ops backup run web-data
```

For the complete walk-through (including shell completions and provisioning templates), see the [Getting Started Tutorial](docs/tutorial/getting_started.md).

---

## Installation

### Pre-built Binaries (Recommended)
Download the latest archive for your platform from [Releases](https://github.com/volcano6/opspulse/releases):

```bash
# Example for linux-amd64 (replace v0.4.0 with the latest release tag):
tar -xzf opspulse-v0.4.0-linux-amd64.tar.gz
grep 'linux-amd64' checksums.txt | sha256sum -c -
sudo install -m 0755 opspulse /usr/local/bin/ops
ops version
```

Every official release includes SPDX JSON SBOMs and is signed with GitHub Build Provenance:
```bash
gh attestation verify opspulse-v0.4.0-linux-amd64.tar.gz --repo volcano6/opspulse
```

### Build from Source
Requires **Go 1.26+**:

```bash
git clone https://github.com/volcano6/opspulse.git && cd opspulse
make install              # Builds and installs 'ops' to your PATH
ops completion --install  # Automatically registers shell completions (Bash, Zsh, Fish, PowerShell)
```

---

## Roadmap

- **v0.5.0 (Next)**: Release hardening, broader migration tests, and operational polish.
- **v0.6.0**: Architecture modularization (`ssh.go` decoupling) and daemon productionization (systemd unit / Windows Task Scheduler).
- **v0.7.0**: Deep WSL integration, automated SSH topology bootstrapping, and zero-latency local status overview.
- **v0.8.0**: Docker migration coverage and preflight hardening beyond the current `plan` / `apply` flow.

---

## Documentation Map

- **Tutorials**: [Getting Started](docs/tutorial/getting_started.md), [Container Migration & WSL Backup](docs/tutorial/migration.md), [1Password Offline Verification](docs/tutorial/onepassword_offline_verify.md)
- **Reference**: [Command Line Reference](docs/reference/cli.md), [Backup & Restore Guide](docs/reference/backup.md), [Configuration & Templates](docs/reference/configuration.md), [Servers & Assets](docs/reference/server_ops.md), [1Password Integration](docs/reference/onepassword.md)
- **Architecture & Security**: [Architecture & Trust Model](docs/explanation/architecture.md), [Security Policy](SECURITY.md)
- **Governance**: [Changelog](CHANGELOG.md), [Contributing Guidelines](CONTRIBUTING.md)

---

## Security Model & Non-Goals

- **Not a Puppet/Ansible Replacement**: OpsPulse performs single-shot template bootstrapping and command execution; it does not enforce continuous state convergence.
- **Not a Cloud Provisioner (Terraform)**: OpsPulse manages existing servers and infrastructure; it does not provision cloud VMs.
- **Not a Monitoring Platform**: OpsPulse fires discrete webhook events on job completion; it does not store time-series metrics.
- **Root of Trust**: Local workstation disk (`servers.yaml` with `0600` permissions). Unknown host keys fail closed by default. SSH keys are preferred; passwords, when used, are stored as plaintext in the local inventory and should be removed with `ops server setup-key <name> --remove-password` after key authentication is verified.

---

## License

Open-source software licensed under the [Apache License 2.0](LICENSE).
