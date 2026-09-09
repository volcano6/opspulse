#!/bin/bash
# ---
# name: bbr
# version: 1
# os: [ubuntu, debian]
# description: Enable TCP BBR congestion control and fq queuing discipline
# ---
set -euo pipefail

echo "==> Checking TCP BBR congestion control status..."
if sysctl net.ipv4.tcp_congestion_control 2>/dev/null | grep -q bbr; then
    echo "ℹ️ TCP BBR is already enabled. Current status:"
    sysctl net.ipv4.tcp_congestion_control net.core.default_qdisc 2>/dev/null || true
    exit 0
fi

echo "==> Configuring TCP BBR in /etc/sysctl.d/99-bbr.conf..."
mkdir -p /etc/sysctl.d
cat << 'EOF' > /etc/sysctl.d/99-bbr.conf
net.core.default_qdisc=fq
net.ipv4.tcp_congestion_control=bbr
EOF

sysctl -p /etc/sysctl.d/99-bbr.conf >/dev/null 2>&1 || sysctl -p >/dev/null 2>&1 || true

echo "==> Verifying TCP BBR activation..."
if sysctl net.ipv4.tcp_congestion_control 2>/dev/null | grep -q bbr; then
    echo "=========================================================="
    echo "✅ TCP BBR congestion control enabled successfully!"
    sysctl net.ipv4.tcp_congestion_control net.core.default_qdisc 2>/dev/null || true
    echo "=========================================================="
else
    echo "⚠️ Warning: Failed to activate BBR. Kernel may lack BBR module support." >&2
    exit 1
fi
