#!/bin/bash
# ---
# name: caddy
# version: 1
# os: [ubuntu, debian]
# description: Install official Caddy Web server with automatic HTTPS reverse proxy
# ---
set -euo pipefail

echo "==> Setting up official Caddy repository..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl gnupg

curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg --yes
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | tee /etc/apt/sources.list.d/caddy-stable.list > /dev/null

echo "==> Installing Caddy..."
apt-get update -y
apt-get install -y caddy

echo "==> Enabling and starting Caddy service..."
systemctl enable --now caddy

echo "==> Verifying Caddy version..."
caddy version

echo "==> Caddy installed and started successfully."
