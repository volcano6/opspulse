#!/bin/bash
# ---
# name: nodejs
# version: 1
# os: [ubuntu, debian]
# description: Install Node.js LTS (or specified major version e.g. -t nodejs:22) with npm and corepack
# ---
set -euo pipefail

SPEC_VERSION="${1:-${SCRIPT_ARG:-22}}"
# Extract digits only (e.g. "22", "v22", "22.x" -> "22")
NODE_MAJOR=$(echo "$SPEC_VERSION" | tr -dc '0-9')
if [ -z "$NODE_MAJOR" ]; then
    NODE_MAJOR="22"
fi

if command -v node >/dev/null 2>&1; then
    CURRENT_VERSION=$(node -v 2>/dev/null | sed 's/^v//' | cut -d. -f1 || true)
    if [ "$CURRENT_VERSION" = "$NODE_MAJOR" ]; then
        echo "==> Node.js v${NODE_MAJOR} (detected: $(node -v)) is already installed. Skipping package installation."
        echo "--> Node path: $(command -v node)"
        node -v
        npm -v
        exit 0
    else
        echo "==> Existing Node.js version is v${CURRENT_VERSION}, switching/upgrading to Node.js v${NODE_MAJOR}..."
    fi
fi

echo "==> 1. Setting up prerequisites and NodeSource repository for Node.js ${NODE_MAJOR}.x..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y ca-certificates curl gnupg

SETUP_URL="https://deb.nodesource.com/setup_${NODE_MAJOR}.x"
TMP_SETUP=$(mktemp)

if ! curl -fsSL --connect-timeout 10 -m 60 "$SETUP_URL" -o "$TMP_SETUP" 2>/dev/null; then
    echo "--> Direct download timed out, retrying with extended timeout..."
    curl -fsSL --connect-timeout 15 -m 120 "$SETUP_URL" -o "$TMP_SETUP"
fi

bash "$TMP_SETUP"
rm -f "$TMP_SETUP"

echo "==> 2. Installing Node.js..."
apt-get update -y
apt-get install -y nodejs

echo "==> 3. Enabling corepack for modern package managers (pnpm, yarn)..."
if command -v corepack >/dev/null 2>&1; then
    corepack enable 2>/dev/null || true
fi

TARGET_USER="${SUDO_USER:-$(whoami)}"
TARGET_HOME="$(getent passwd "$TARGET_USER" 2>/dev/null | cut -d: -f6 || echo "$HOME")"
[ -d "$TARGET_HOME" ] || TARGET_HOME="$HOME"

if [ "$TARGET_USER" != "root" ] && [ -d "$TARGET_HOME" ]; then
    echo "==> 4. Configuring npm global directory for user $TARGET_USER (~/.npm-global)..."
    mkdir -p "$TARGET_HOME/.npm-global"
    chown -R "$TARGET_USER:$(id -gn "$TARGET_USER" 2>/dev/null || echo "$TARGET_USER")" "$TARGET_HOME/.npm-global" 2>/dev/null || true
    su - "$TARGET_USER" -c "npm config set prefix '$TARGET_HOME/.npm-global'" 2>/dev/null || \
        npm config set prefix "$TARGET_HOME/.npm-global" --userconfig "$TARGET_HOME/.npmrc" 2>/dev/null || true
fi

echo "==> Verifying Node.js and npm versions..."
node -v
npm -v

echo "=========================================================="
echo "🎉 Node.js v${NODE_MAJOR} LTS installation completed!"
echo "=========================================================="
