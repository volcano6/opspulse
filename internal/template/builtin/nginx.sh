#!/bin/bash
# ---
# name: nginx
# version: 1
# os: [ubuntu, debian]
# description: Install official Nginx Web server with systemd service enabled
# ---
set -euo pipefail

if command -v nginx >/dev/null 2>&1; then
    echo "==> Nginx is already installed. Skipping package installation."
    nginx -v
    exit 0
fi

echo "==> Setting up Nginx..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y curl gnupg2 ca-certificates lsb-release

OS_ID=$(. /etc/os-release && echo "$ID")
OS_CODENAME=$(. /etc/os-release && echo "$VERSION_CODENAME")

echo "==> Configuring official Nginx repository for ${OS_ID}/${OS_CODENAME}..."
curl -fsSL https://nginx.org/keys/nginx_signing.key | gpg --dearmor -o /usr/share/keyrings/nginx-archive-keyring.gpg --yes 2>/dev/null || true

if [ -s /usr/share/keyrings/nginx-archive-keyring.gpg ]; then
    echo "deb [signed-by=/usr/share/keyrings/nginx-archive-keyring.gpg] http://nginx.org/packages/${OS_ID} ${OS_CODENAME} nginx" > /etc/apt/sources.list.d/nginx.list
    cat << 'EOF' > /etc/apt/preferences.d/99nginx
Package: *
Pin: origin nginx.org
Pin: release o=nginx
Pin-Priority: 900
EOF
fi

apt-get update -y
if ! apt-get install -y nginx; then
    echo "--> Failed to install from official repo, falling back to distribution package..."
    rm -f /etc/apt/sources.list.d/nginx.list /etc/apt/preferences.d/99nginx
    apt-get update -y
    apt-get install -y nginx
fi

echo "==> Enabling and starting Nginx service..."
systemctl enable --now nginx

if command -v ufw >/dev/null 2>&1 && ufw status | grep -qw "active"; then
    echo "==> Allowing standard Web ports in UFW..."
    ufw allow 80/tcp comment 'Nginx HTTP' || true
    ufw allow 443/tcp comment 'Nginx HTTPS' || true
fi

echo "==> Verifying Nginx version..."
nginx -v

echo "==> Nginx installed and started successfully."
