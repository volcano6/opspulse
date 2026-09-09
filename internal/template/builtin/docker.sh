#!/bin/bash
# ---
# name: docker
# version: 3
# os: [ubuntu, debian]
# description: Install Docker CE and Docker Compose plugin with automatic mirror fallback
# ---
set -euo pipefail

# Fast-path idempotency: if docker and docker compose are already installed, skip package setup
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    echo "==> Docker and Docker Compose plugin are already installed. Skipping package installation."
else
    echo "==> Setting up Docker repository..."
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -y
    apt-get install -y ca-certificates curl gnupg

    install -m 0755 -d /etc/apt/keyrings

    OS_ID=$(. /etc/os-release && echo "$ID")
    OS_CODENAME=$(. /etc/os-release && echo "$VERSION_CODENAME")
    ARCH=$(dpkg --print-architecture)

    # List of mirror endpoints to try in priority order:
    # Supports automatic fallback if the official Docker repo is blocked/reset (e.g. in Mainland China).
    MIRROR_BASES=(
        "https://download.docker.com/linux"
        "https://mirrors.tencent.com/docker-ce/linux"
        "https://mirrors.aliyun.com/docker-ce/linux"
        "https://mirrors.ustc.edu.cn/docker-ce/linux"
    )

    # If an argument is provided (e.g. docker:tencent or docker:aliyun), prioritize it
    if [ -n "${SCRIPT_ARG:-}" ]; then
        case "${SCRIPT_ARG}" in
            tencent)
                MIRROR_BASES=("https://mirrors.tencent.com/docker-ce/linux" "${MIRROR_BASES[@]}")
                ;;
            aliyun)
                MIRROR_BASES=("https://mirrors.aliyun.com/docker-ce/linux" "${MIRROR_BASES[@]}")
                ;;
            ustc)
                MIRROR_BASES=("https://mirrors.ustc.edu.cn/docker-ce/linux" "${MIRROR_BASES[@]}")
                ;;
        esac
    fi

    SELECTED_MIRROR=""
    for mirror in "${MIRROR_BASES[@]}"; do
        echo "==> Testing Docker repository mirror: ${mirror}/${OS_ID}..."
        if curl -fsSL --connect-timeout 5 --max-time 15 "${mirror}/${OS_ID}/gpg" | gpg --dearmor -o /etc/apt/keyrings/docker.gpg --yes 2>/dev/null; then
            if [ -s /etc/apt/keyrings/docker.gpg ]; then
                chmod a+r /etc/apt/keyrings/docker.gpg
                SELECTED_MIRROR="$mirror"
                echo "==> Successfully retrieved Docker GPG key from ${mirror}"
                break
            fi
        fi
        echo "==> Mirror ${mirror} failed or connection reset, trying next..."
    done

    if [ -z "$SELECTED_MIRROR" ]; then
        echo "Error: Failed to fetch Docker GPG key from all mirror sources." >&2
        exit 1
    fi

    echo \
      "deb [arch=${ARCH} signed-by=/etc/apt/keyrings/docker.gpg] ${SELECTED_MIRROR}/${OS_ID} \
      ${OS_CODENAME} stable" | \
      tee /etc/apt/sources.list.d/docker.list > /dev/null

    echo "==> Installing Docker packages from ${SELECTED_MIRROR}..."
    apt-get update -y
    apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
fi

echo "==> Configuring Docker daemon options (log rotation)..."
mkdir -p /etc/docker

OPS_DEFAULTS='{"log-driver":"json-file","log-opts":{"max-size":"10m","max-file":"3"}}'

if [ ! -f /etc/docker/daemon.json ]; then
    echo "$OPS_DEFAULTS" > /etc/docker/daemon.json
    echo "✅ Created /etc/docker/daemon.json with default log rotation."
elif command -v jq >/dev/null 2>&1; then
    MERGED=$(jq -s '.[0] * .[1]' <(echo "$OPS_DEFAULTS") /etc/docker/daemon.json)
    echo "$MERGED" > /etc/docker/daemon.json
    echo "✅ Merged log rotation defaults into existing /etc/docker/daemon.json."
elif command -v python3 >/dev/null 2>&1; then
    python3 -c "
import json
defaults = json.loads('$OPS_DEFAULTS')
with open('/etc/docker/daemon.json') as f:
    existing = json.load(f)
merged = {**defaults, **existing}
if 'log-opts' in defaults and 'log-opts' in existing:
    merged['log-opts'] = {**defaults['log-opts'], **existing['log-opts']}
with open('/etc/docker/daemon.json', 'w') as f:
    json.dump(merged, f, indent=2)
"
    echo "✅ Merged log rotation defaults (via python3 fallback)."
else
    echo "⚠️  /etc/docker/daemon.json already exists but jq/python3 not available for safe merging. Skipping."
fi

echo "==> Configuring Docker service and auto-start on boot..."
if ps -p 1 -o comm= | grep -q systemd || [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
    systemctl enable --now docker.service containerd.service || true
else
    echo "⚠️  systemd not detected (WSL/SysV init). Starting Docker via service..."
    service docker start || true
fi

echo "==> Configuring Docker user permissions..."
groupadd -f docker

# Automatically grant non-root users access to Docker daemon without sudo (deduplicated)
USERS_TO_ADD=""
if [ -n "${OPS_SERVER_USER:-}" ] && [ "${OPS_SERVER_USER}" != "root" ]; then
    USERS_TO_ADD="${USERS_TO_ADD} ${OPS_SERVER_USER}"
fi
if [ -n "${SUDO_USER:-}" ] && [ "${SUDO_USER}" != "root" ]; then
    USERS_TO_ADD="${USERS_TO_ADD} ${SUDO_USER}"
fi

for u in $(echo "${USERS_TO_ADD}" | tr ' ' '\n' | sort -u | grep -v '^$'); do
    if id -u "$u" >/dev/null 2>&1; then
        echo "==> Adding user '${u}' to docker group..."
        usermod -aG docker "$u"
    fi
done

if [ -S /var/run/docker.sock ]; then
    chown root:docker /var/run/docker.sock || true
    chmod 660 /var/run/docker.sock || true
fi

echo "==> Verifying Docker installation & service status..."
systemctl is-active docker
systemctl is-enabled docker
docker --version
docker compose version

echo "==> Docker installed and configured successfully. (Docker group membership active on next login session)"
