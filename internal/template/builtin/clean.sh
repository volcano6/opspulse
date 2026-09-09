#!/bin/bash
# ---
# name: clean
# version: 2
# os: [ubuntu, debian]
# description: Clean up APT cache, old systemd journal logs, and unused Docker resources
# ---
set -euo pipefail

echo "==> 1. Cleaning APT package cache..."
export DEBIAN_FRONTEND=noninteractive
apt-get autoremove --purge -y && apt-get clean

echo "==> 2. Vacuuming Systemd journal logs older than 7 days..."
if command -v journalctl &>/dev/null; then
    journalctl --vacuum-time=7d || true
fi

if command -v docker &>/dev/null; then
    echo "==> 3. Pruning unused Docker images, containers, and networks (older than 24h)..."
    docker system prune -f --filter "until=24h" 2>/dev/null || docker system prune -f || true
fi

echo "=========================================================="
echo "✅ Disk cleanup completed successfully! Current disk usage:"
df -h /
echo "=========================================================="
