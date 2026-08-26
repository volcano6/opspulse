#!/bin/bash
# ---
# name: clean
# version: 1
# os: [ubuntu, debian]
# description: Clean up APT cache, old systemd journal logs, and unused Docker resources
# ---
set -euo pipefail

echo "==> 1. Cleaning APT package cache..."
export DEBIAN_FRONTEND=noninteractive
apt-get autoremove -y && apt-get clean

echo "==> 2. Vacuuming Systemd journal logs older than 3 days..."
if command -v journalctl &>/dev/null; then
    journalctl --vacuum-time=3d || true
fi

if command -v docker &>/dev/null; then
    echo "==> 3. Pruning unused Docker images, containers, and networks..."
    docker system prune -f || true
fi

echo "=========================================================="
echo "✅ Disk cleanup completed successfully! Current disk usage:"
df -h /
echo "=========================================================="
