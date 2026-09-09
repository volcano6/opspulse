#!/bin/bash
# ---
# name: golang
# version: 1
# os: [ubuntu, debian]
# description: Install Go programming language toolchain (e.g. -t golang:1.24.0 or default latest)
# ---
set -euo pipefail

SPEC_VERSION="${1:-${SCRIPT_ARG:-}}"

if [ -z "$SPEC_VERSION" ]; then
    echo "==> Detecting latest stable Go version..."
    LATEST=$(curl -fsSL --connect-timeout 5 https://go.dev/VERSION?m=text 2>/dev/null | head -n 1 || true)
    GO_VERSION="${LATEST:-go1.24.0}"
else
    if [[ "$SPEC_VERSION" == go* ]]; then
        GO_VERSION="$SPEC_VERSION"
    else
        GO_VERSION="go${SPEC_VERSION}"
    fi
fi

CLEAN_VERSION="${GO_VERSION#go}"

if command -v go >/dev/null 2>&1; then
    CURRENT_VERSION=$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//' || true)
    if [ "$CURRENT_VERSION" = "$CLEAN_VERSION" ]; then
        echo "==> Go ${CLEAN_VERSION} is already installed. Skipping installation."
        go version
        exit 0
    fi
fi

ARCH=$(uname -m)
case "$ARCH" in
    x86_64)        GO_ARCH="amd64" ;;
    aarch64|arm64) GO_ARCH="arm64" ;;
    *)
        echo "Error: Unsupported architecture for Go: $ARCH" >&2
        exit 1
        ;;
esac

TARBALL="${GO_VERSION}.linux-${GO_ARCH}.tar.gz"

DOWNLOAD_URLS=(
    "https://golang.google.cn/dl/${TARBALL}"
    "https://go.dev/dl/${TARBALL}"
)

TMP_DIR=$(mktemp -d)
DOWNLOAD_SUCCESS=false

for url in "${DOWNLOAD_URLS[@]}"; do
    echo "==> Downloading Go from ${url}..."
    if curl -fsSL --connect-timeout 10 -m 120 "$url" -o "${TMP_DIR}/${TARBALL}"; then
        DOWNLOAD_SUCCESS=true
        break
    fi
    echo "--> Download failed or timed out, trying next mirror..."
done

if [ "$DOWNLOAD_SUCCESS" != "true" ]; then
    echo "Error: Failed to download Go binary package from all mirrors." >&2
    rm -rf "$TMP_DIR"
    exit 1
fi

echo "==> Extracting Go toolchain to /usr/local/go..."
rm -rf /usr/local/go
tar -C /usr/local -xzf "${TMP_DIR}/${TARBALL}"
rm -rf "$TMP_DIR"

echo "==> Creating system symlinks and environment profile..."
ln -sf /usr/local/go/bin/go /usr/local/bin/go
ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt

cat << 'EOF' > /etc/profile.d/golang.sh
export PATH=$PATH:/usr/local/go/bin
export GOPATH=$HOME/go
export PATH=$PATH:$GOPATH/bin
EOF

echo "==> Verifying Go installation..."
/usr/local/bin/go version

echo "=========================================================="
echo "✅ Go ${CLEAN_VERSION} installed successfully!"
echo "=========================================================="
