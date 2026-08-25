#!/bin/bash
# ---
# name: security
# version: 1
# os: [ubuntu, debian]
# description: Basic security hardening (UFW firewall, fail2ban)
# ---
set -euo pipefail

echo "==> Configuring UFW firewall..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y ufw fail2ban

# Allow current SSH port
SSH_PORT=$(ss -tlnp 2>/dev/null | grep sshd | awk '{print $4}' | awk -F':' '{print $NF}' | head -n1 || true)
if [ -z "$SSH_PORT" ]; then
    SSH_PORT=22
fi

echo "==> Detected SSH port: ${SSH_PORT}"
ufw allow "${SSH_PORT}/tcp" comment 'SSH'
ufw default deny incoming
ufw default allow outgoing
ufw --force enable

echo "==> Enabling Fail2ban..."
systemctl enable --now fail2ban

echo "==> Security hardening completed successfully."
