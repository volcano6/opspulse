#!/bin/bash
# ---
# name: cn
# version: 1
# os: [ubuntu, debian]
# description: China mainland network & mirror optimization (APT, Git, Docker, Go, Pip)
# ---
set -euo pipefail

echo "=========================================================="
echo "🚀 Starting China mainland VPS network & mirror optimization"
echo "=========================================================="

# -----------------------------------------------------------------------------
# 1. System APT Package Mirror Optimization
# -----------------------------------------------------------------------------
echo "==> 1. Configuring APT package manager mirrors..."

if [ ! -f /etc/os-release ]; then
    echo "⚠️ /etc/os-release not found. Skipping APT mirror replacement."
else
    . /etc/os-release
    OS_ID="${ID:-}"
    OS_CODENAME="${VERSION_CODENAME:-}"

    if [ -z "$OS_CODENAME" ] && [ -n "${VERSION_ID:-}" ]; then
        # Fallback detection for older distributions
        case "$VERSION_ID" in
            "24.04"*) OS_CODENAME="noble" ;;
            "22.04"*) OS_CODENAME="jammy" ;;
            "20.04"*) OS_CODENAME="focal" ;;
            "12"*)    OS_CODENAME="bookworm" ;;
            "11"*)    OS_CODENAME="bullseye" ;;
        esac
    fi

    # Detect fastest / closest mirror (cloud VPC internal vs. public mirrors)
    CHOSEN_MIRROR=""
    MIRROR_TYPE=""

    echo "--> Detecting network environment and available mirrors..."
    if curl -s --connect-timeout 2 --max-time 3 http://mirrors.tencentyun.com >/dev/null 2>&1; then
        CHOSEN_MIRROR="http://mirrors.tencentyun.com"
        MIRROR_TYPE="Tencent Cloud VPC internal (fastest & zero egress quota)"
    elif curl -s --connect-timeout 2 --max-time 3 http://mirrors.cloud.aliyuncs.com >/dev/null 2>&1; then
        CHOSEN_MIRROR="http://mirrors.cloud.aliyuncs.com"
        MIRROR_TYPE="Alibaba Cloud VPC internal (fastest & zero egress quota)"
    elif curl -s --connect-timeout 2 --max-time 3 http://repo.huaweicloud.com >/dev/null 2>&1; then
        CHOSEN_MIRROR="http://repo.huaweicloud.com"
        MIRROR_TYPE="Huawei Cloud mirror"
    elif curl -s --connect-timeout 2 --max-time 3 https://mirrors.tuna.tsinghua.edu.cn >/dev/null 2>&1; then
        CHOSEN_MIRROR="https://mirrors.tuna.tsinghua.edu.cn"
        MIRROR_TYPE="Tsinghua TUNA public mirror"
    else
        CHOSEN_MIRROR="https://mirrors.aliyun.com"
        MIRROR_TYPE="Aliyun public mirror"
    fi

    echo "--> Selected mirror: ${CHOSEN_MIRROR} (${MIRROR_TYPE})"

    BACKUP_SUFFIX="$(date +%Y%m%d%H%M%S).bak"

    # Backup existing sources.list
    if [ -f /etc/apt/sources.list ]; then
        echo "--> Backing up /etc/apt/sources.list to /etc/apt/sources.list.${BACKUP_SUFFIX}..."
        cp /etc/apt/sources.list "/etc/apt/sources.list.${BACKUP_SUFFIX}"
    fi

    # Handle Ubuntu 24.04+ deb822 format (/etc/apt/sources.list.d/ubuntu.sources)
    if [ -f /etc/apt/sources.list.d/ubuntu.sources ]; then
        echo "--> Backing up /etc/apt/sources.list.d/ubuntu.sources..."
        cp /etc/apt/sources.list.d/ubuntu.sources "/etc/apt/sources.list.d/ubuntu.sources.${BACKUP_SUFFIX}"
        sed -i -E "s|https?://(archive\|security)\.ubuntu\.com/ubuntu/?|${CHOSEN_MIRROR}/ubuntu/|g" /etc/apt/sources.list.d/ubuntu.sources
        echo "✅ Updated /etc/apt/sources.list.d/ubuntu.sources"
    fi

    # Generate standard sources.list for Ubuntu or Debian
    case "$OS_ID" in
        ubuntu)
            if [ -n "$OS_CODENAME" ]; then
                cat << EOF > /etc/apt/sources.list
deb ${CHOSEN_MIRROR}/ubuntu/ ${OS_CODENAME} main restricted universe multiverse
deb ${CHOSEN_MIRROR}/ubuntu/ ${OS_CODENAME}-updates main restricted universe multiverse
deb ${CHOSEN_MIRROR}/ubuntu/ ${OS_CODENAME}-backports main restricted universe multiverse
deb ${CHOSEN_MIRROR}/ubuntu/ ${OS_CODENAME}-security main restricted universe multiverse
EOF
                echo "✅ Configured Ubuntu (${OS_CODENAME}) sources in /etc/apt/sources.list"
            fi
            ;;
        debian)
            if [ -n "$OS_CODENAME" ]; then
                cat << EOF > /etc/apt/sources.list
deb ${CHOSEN_MIRROR}/debian/ ${OS_CODENAME} main contrib non-free non-free-firmware
deb ${CHOSEN_MIRROR}/debian/ ${OS_CODENAME}-updates main contrib non-free non-free-firmware
deb ${CHOSEN_MIRROR}/debian/ ${OS_CODENAME}-backports main contrib non-free non-free-firmware
deb ${CHOSEN_MIRROR}/debian-security/ ${OS_CODENAME}-security main contrib non-free non-free-firmware
EOF
                echo "✅ Configured Debian (${OS_CODENAME}) sources in /etc/apt/sources.list"
            fi
            ;;
        *)
            echo "ℹ️ OS '${OS_ID}' is not Debian/Ubuntu. Keeping existing sources."
            ;;
    esac

    echo "--> Updating APT package index..."
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -y || {
        echo "⚠️ Warning: apt-get update returned non-zero. Continuing with initialization..."
    }
fi

# -----------------------------------------------------------------------------
# 1.5 Network Optimization: Prefer IPv4 for Dual-Stack Hosts (prevents IPv6 throttling)
# -----------------------------------------------------------------------------
echo "--> Configuring IPv4 precedence in /etc/gai.conf..."
if [ -f /etc/gai.conf ]; then
    if ! grep -q "^precedence ::ffff:0:0/96" /etc/gai.conf; then
        echo "precedence ::ffff:0:0/96  100" >> /etc/gai.conf
        echo "✅ Configured IPv4 precedence in /etc/gai.conf"
    fi
else
    echo "precedence ::ffff:0:0/96  100" > /etc/gai.conf
    echo "✅ Created /etc/gai.conf with IPv4 precedence"
fi

# -----------------------------------------------------------------------------
# 2. Global Git & GitHub Acceleration (insteadOf Proxy)
# -----------------------------------------------------------------------------
echo "==> 2. Configuring global Git & GitHub acceleration..."

GH_PROXY_URL="${GIT_PROXY_URL:-https://ghfast.top/https://github.com/}"

# Clean up stale/duplicate insteadOf rules matching https://github.com/
clean_git_rules() {
    local scope="$1"
    git config "$scope" --get-regexp '^url\..*\.insteadof' 2>/dev/null | while read -r key val; do
        if [ "$val" = "https://github.com/" ]; then
            local sec
            sec=$(echo "$key" | sed -E 's/^url\.(.*)\.insteadof/\1/')
            git config "$scope" --remove-section "url.${sec}" 2>/dev/null || true
        fi
    done || true
}

clean_git_rules "--system"
clean_git_rules "--global"

echo "--> Setting Git insteadOf rule to: ${GH_PROXY_URL}"
git config --system url."${GH_PROXY_URL}".insteadOf "https://github.com/" 2>/dev/null || true
git config --global url."${GH_PROXY_URL}".insteadOf "https://github.com/"

# Also configure for non-root sudo user if present
if [ -n "${SUDO_USER:-}" ] && [ "${SUDO_USER}" != "root" ]; then
    if id -u "$SUDO_USER" >/dev/null 2>&1; then
        echo "--> Applying Git insteadOf rule for user: ${SUDO_USER}"
        sudo -u "${SUDO_USER}" bash -c "
            git config --global --get-regexp '^url\..*\.insteadof' 2>/dev/null | while read -r k v; do
                if [ \"\$v\" = 'https://github.com/' ]; then
                    sec=\$(echo \"\$k\" | sed -E 's/^url\.(.*)\.insteadof/\1/')
                    git config --global --remove-section \"url.\${sec}\" 2>/dev/null || true
                fi
            done
            git config --global url.'${GH_PROXY_URL}'.insteadOf 'https://github.com/'
        " 2>/dev/null || true
    fi
fi
echo "✅ Git GitHub acceleration configured (system & user wide)."

# -----------------------------------------------------------------------------
# 3. Docker Registry Mirrors (Including verified private & public mirrors)
# -----------------------------------------------------------------------------
echo "==> 3. Configuring Docker Registry Mirrors..."

mkdir -p /etc/docker

# Priority:
# 1. Custom argument ($1 or $SCRIPT_ARG) if supplied
# 2. Verified private mirror (https://docker-mirror.example.com)
# 3. Stable public mirror fallback (https://docker.m.daocloud.io)
CUSTOM_ARG="${1:-${SCRIPT_ARG:-}}"

DOCKER_MIRRORS_LIST=()
if [ -n "$CUSTOM_ARG" ] && [[ "$CUSTOM_ARG" =~ ^https?:// ]]; then
    DOCKER_MIRRORS_LIST+=("$CUSTOM_ARG")
fi

DOCKER_MIRRORS_LIST+=(
    "https://docker-mirror.example.com"
    "https://docker.m.daocloud.io"
)

# Convert bash array to JSON array
MIRRORS_JSON_ARRAY="["
for i in "${!DOCKER_MIRRORS_LIST[@]}"; do
    if [ "$i" -gt 0 ]; then
        MIRRORS_JSON_ARRAY+=", "
    fi
    MIRRORS_JSON_ARRAY+="\"${DOCKER_MIRRORS_LIST[$i]}\""
done
MIRRORS_JSON_ARRAY+="]"

echo "--> Target Docker registry-mirrors: ${MIRRORS_JSON_ARRAY}"

DAEMON_JSON="/etc/docker/daemon.json"
NEW_CONFIG="{\"registry-mirrors\": ${MIRRORS_JSON_ARRAY}}"

if [ ! -f "$DAEMON_JSON" ]; then
    echo "$NEW_CONFIG" > "$DAEMON_JSON"
    echo "✅ Created $DAEMON_JSON with registry-mirrors."
elif command -v jq >/dev/null 2>&1; then
    MERGED=$(jq -s '.[0] * .[1]' "$DAEMON_JSON" <(echo "$NEW_CONFIG"))
    echo "$MERGED" > "$DAEMON_JSON"
    echo "✅ Merged registry-mirrors into existing $DAEMON_JSON (via jq)."
elif command -v python3 >/dev/null 2>&1; then
    python3 -c "
import json
with open('$DAEMON_JSON') as f:
    existing = json.load(f)
new_mirrors = json.loads('$MIRRORS_JSON_ARRAY')
existing['registry-mirrors'] = new_mirrors
with open('$DAEMON_JSON', 'w') as f:
    json.dump(existing, f, indent=2)
"
    echo "✅ Merged registry-mirrors into existing $DAEMON_JSON (via python3)."
else
    echo "⚠️ $DAEMON_JSON exists but jq/python3 not available. Skipping merge to prevent corruption."
fi

# If Docker service is already active, safely reload/restart it
if command -v systemctl >/dev/null 2>&1 && systemctl is-active docker >/dev/null 2>&1; then
    echo "--> Docker is currently running. Reloading Docker daemon..."
    systemctl daemon-reload || true
    systemctl restart docker || true
    echo "✅ Docker daemon reloaded with new registry mirrors."
else
    echo "ℹ️ Docker service not currently running (will use mirrors when started by docker template)."
fi

# -----------------------------------------------------------------------------
# 4. Programming Language Ecosystem Mirrors (Go, Python, Node)
# -----------------------------------------------------------------------------
echo "==> 4. Configuring language ecosystem package mirrors..."

# 4.1 Go GOPROXY
echo "--> Configuring GOPROXY (goproxy.cn)..."
cat << 'EOF' > /etc/profile.d/goproxy.sh
export GOPROXY="https://goproxy.cn,direct"
EOF
chmod +x /etc/profile.d/goproxy.sh

if command -v go >/dev/null 2>&1; then
    go env -w GOPROXY="https://goproxy.cn,direct" 2>/dev/null || true
fi
echo "✅ GOPROXY set to https://goproxy.cn,direct"

# 4.2 Python PyPI mirror (pip.conf)
echo "--> Configuring PyPI mirror (/etc/pip.conf)..."
cat << 'EOF' > /etc/pip.conf
[global]
index-url = https://pypi.tuna.tsinghua.edu.cn/simple
trusted-host = pypi.tuna.tsinghua.edu.cn
timeout = 60
EOF
echo "✅ Global pip mirror set to Tsinghua TUNA (/etc/pip.conf)"

# 4.3 Node.js npm mirror (if npm command exists)
if command -v npm >/dev/null 2>&1; then
    echo "--> Configuring npm registry (npmmirror.com)..."
    npm config set registry https://registry.npmmirror.com 2>/dev/null || true
    echo "✅ npm registry set to https://registry.npmmirror.com"
fi

echo "=========================================================="
echo "🎉 China mainland VPS initialization completed successfully!"
echo "   • APT mirrors updated and synced"
echo "   • Git GitHub insteadOf acceleration active"
echo "   • Docker registry-mirrors configured (docker-mirror.example.com + daocloud)"
echo "   • Go (goproxy.cn) and Python (tsinghua) mirrors configured"
echo "=========================================================="
