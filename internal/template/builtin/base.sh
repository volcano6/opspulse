#!/bin/bash
# ---
# name: base
# version: 1
# os: [ubuntu, debian]
# description: Install essential system tools and common packages
# ---
set -euo pipefail

echo "==> Updating package list..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -y

echo "==> Installing base packages..."
apt-get install -y --no-install-recommends \
    curl \
    wget \
    git \
    vim \
    htop \
    jq \
    ca-certificates \
    gnupg \
    lsb-release \
    tzdata \
    ufw \
    fail2ban

echo "==> Base installation completed successfully."
