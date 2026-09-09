#!/bin/bash
# ---
# name: firewall-ports
# version: 1
# os: [ubuntu, debian]
# description: Open custom firewall ports in UFW (e.g. -t firewall-ports:80,443,8080 or -t firewall-ports:51820/udp)
# ---
set -euo pipefail

RAW_PORTS="${1:-${SCRIPT_ARG:-}}"

if [ -z "$RAW_PORTS" ]; then
    echo "Error: No ports specified for firewall-ports template." >&2
    echo "Usage examples:" >&2
    echo "  ops bootstrap <server> -t firewall-ports:80,443,8080" >&2
    echo "  ops bootstrap <server> -t firewall-ports:8080/tcp,51820/udp" >&2
    exit 1
fi

if ! command -v ufw >/dev/null 2>&1; then
    echo "==> UFW is not installed. Installing ufw..."
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -y
    apt-get install -y ufw
fi

# Ensure UFW is enabled if not already
if ! ufw status | grep -qw "active"; then
    echo "==> Enabling UFW..."
    ufw --force enable
fi

echo "==> Configuring firewall rules for: ${RAW_PORTS}..."

# Replace commas with spaces to iterate
IFS=',' read -ra PORT_LIST <<< "$RAW_PORTS"
for entry in "${PORT_LIST[@]}"; do
    # Trim whitespace
    entry=$(echo "$entry" | xargs)
    if [ -z "$entry" ]; then
        continue
    fi

    echo "  -> Allowing port: ${entry}"
    ufw allow "$entry" comment 'OpsPulse Custom'
done

echo "=========================================================="
echo "✅ Firewall ports updated. Current UFW status:"
ufw status numbered
echo "=========================================================="
