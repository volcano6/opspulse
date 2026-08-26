#!/bin/bash
# ---
# name: base
# version: 2
# os: [ubuntu, debian]
# description: Install essential system tools (curl, git, htop, jq, tmux) and enable BBR
# ---
set -euo pipefail

echo "==> Updating package list..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -y

echo "==> Installing essential base packages..."
apt-get install -y --no-install-recommends \
    curl \
    wget \
    git \
    vim \
    nano \
    htop \
    jq \
    tmux \
    tar \
    unzip \
    ca-certificates \
    gnupg \
    lsb-release \
    tzdata

echo "==> Configuring TCP BBR congestion control..."
if sysctl net.ipv4.tcp_congestion_control 2>/dev/null | grep -q bbr; then
    echo "  -> BBR is already enabled."
else
    mkdir -p /etc/sysctl.d
    cat << 'EOF' > /etc/sysctl.d/99-bbr.conf
net.core.default_qdisc=fq
net.ipv4.tcp_congestion_control=bbr
EOF
    sysctl -p /etc/sysctl.d/99-bbr.conf >/dev/null 2>&1 || sysctl -p >/dev/null 2>&1 || true
    echo "  -> BBR enabled successfully."
fi

echo "==> Base installation and BBR optimization completed successfully."
