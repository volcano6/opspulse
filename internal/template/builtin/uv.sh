#!/bin/bash
# ---
# name: uv
# version: 1
# os: [ubuntu, debian]
# description: Install Astral uv (extremely fast Python package and project manager)
# ---
set -euo pipefail

if command -v uv >/dev/null 2>&1; then
    echo "==> uv is already installed. Skipping package installation."
    uv --version
    exit 0
fi

echo "==> Installing prerequisites..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y curl ca-certificates tar

export UV_INSTALL_DIR="/usr/local/bin"

echo "==> Installing Astral uv to ${UV_INSTALL_DIR}..."
INSTALL_SUCCESS=false

if curl -LsSf --connect-timeout 8 --max-time 30 https://astral.sh/uv/install.sh 2>/dev/null | sh; then
    INSTALL_SUCCESS=true
fi

if [ "$INSTALL_SUCCESS" != "true" ]; then
    echo "--> Official installer script timed out or failed, downloading release binary directly..."
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64)        UV_ARCH="x86_64-unknown-linux-musl" ;;
        aarch64|arm64) UV_ARCH="aarch64-unknown-linux-musl" ;;
        *)
            echo "Error: Unsupported architecture for uv: $ARCH" >&2
            exit 1
            ;;
    esac

    TARBALL="uv-${UV_ARCH}.tar.gz"
    RAW_URL="https://github.com/astral-sh/uv/releases/latest/download/${TARBALL}"
    MIRROR_URL="https://ghfast.top/${RAW_URL}"

    TMP_DIR=$(mktemp -d)
    if ! curl -fsSL --connect-timeout 8 -m 30 "$RAW_URL" -o "${TMP_DIR}/${TARBALL}" 2>/dev/null; then
        echo "--> Direct download timed out, trying mirror..."
        curl -fsSL --connect-timeout 10 -m 60 "$MIRROR_URL" -o "${TMP_DIR}/${TARBALL}"
    fi

    tar -xzf "${TMP_DIR}/${TARBALL}" -C "$TMP_DIR"
    install -m 0755 "${TMP_DIR}/uv-${UV_ARCH}/uv" /usr/local/bin/uv
    install -m 0755 "${TMP_DIR}/uv-${UV_ARCH}/uvx" /usr/local/bin/uvx
    rm -rf "$TMP_DIR"
fi

echo "==> Verifying uv installation..."
uv --version
if command -v uvx >/dev/null 2>&1; then
    uvx --version
fi

echo "=========================================================="
echo "✅ Astral uv installed successfully to /usr/local/bin/uv"
echo "=========================================================="
