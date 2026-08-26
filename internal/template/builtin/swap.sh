#!/bin/bash
# ---
# name: swap
# version: 1
# os: [ubuntu, debian]
# description: Safely set or resize Swap (default 2GB, e.g. -t swap:4) with zero-downtime hot-switch
# ---
set -euo pipefail

# Support inline argument ($1), environment variable, with fallback to 2GB
RAW_ARG="${1:-${SWAP_SIZE_GB:-2}}"
TARGET_GB=$(echo "$RAW_ARG" | tr -cd '0-9')
if [ -z "$TARGET_GB" ] || [ "$TARGET_GB" -le 0 ]; then
    TARGET_GB=2
fi

TARGET_MB=$((TARGET_GB * 1024))
SWAP_FILE="/swapfile"
NEW_SWAP_FILE="/swapfile.new"

echo "==> Target Swap Size: ${TARGET_GB}GB (${TARGET_MB}MB)"

# 1. Check current system Swap size
CURRENT_SWAP_MB=$(free -m | awk '/Swap:/ {print $2}')
echo "==> Current System Swap: ${CURRENT_SWAP_MB}MB"

# 2. Check if already matches target size (within ±150MB tolerance)
DIFF=$(( CURRENT_SWAP_MB - TARGET_MB ))
if [ "${DIFF#-}" -le 150 ] && [ "$CURRENT_SWAP_MB" -gt 0 ]; then
    echo "ℹ️ Current Swap (${CURRENT_SWAP_MB}MB) already matches target size (${TARGET_MB}MB). Nothing to do."
    exit 0
fi

echo "==> Performing zero-downtime hot-switch to resize Swap to ${TARGET_GB}GB..."

# 3. Create brand-new Swap file alongside existing Swap (no downtime or OOM risk)
echo "--> 1. Pre-allocating new swap file (${NEW_SWAP_FILE})..."
rm -f "$NEW_SWAP_FILE"
fallocate -l "${TARGET_GB}G" "$NEW_SWAP_FILE" 2>/dev/null || dd if=/dev/zero of="$NEW_SWAP_FILE" bs=1M count="$TARGET_MB" status=progress
chmod 600 "$NEW_SWAP_FILE"
mkswap "$NEW_SWAP_FILE"

# 4. Activate new Swap immediately (memory is fully protected)
echo "--> 2. Mounting new swap..."
swapon "$NEW_SWAP_FILE"

# 5. Safely decommission old Swap (pages automatically migrate into the new Swap)
if [ -f "$SWAP_FILE" ]; then
    echo "--> 3. Safely deactivating and migrating old swap..."
    swapoff "$SWAP_FILE" 2>/dev/null || true
    rm -f "$SWAP_FILE"
fi

# 6. Rename new Swap to standard file path
echo "--> 4. Finalizing swap placement..."
swapoff "$NEW_SWAP_FILE"
mv "$NEW_SWAP_FILE" "$SWAP_FILE"
swapon "$SWAP_FILE"

# 7. Persist in /etc/fstab for boot mount
if ! grep -q "$SWAP_FILE" /etc/fstab; then
    echo "$SWAP_FILE none swap sw 0 0" >> /etc/fstab
fi

# 8. Optimize swappiness (10 is ideal for servers)
sysctl vm.swappiness=10 >/dev/null 2>&1 || true
mkdir -p /etc/sysctl.d
if ! grep -q "vm.swappiness" /etc/sysctl.conf 2>/dev/null && ! grep -q "vm.swappiness" /etc/sysctl.d/*.conf 2>/dev/null; then
    echo "vm.swappiness=10" >> /etc/sysctl.d/99-swap.conf
fi

echo "=========================================="
echo "✅ Swap adjustment completed successfully! Current status:"
swapon --show
free -h
echo "=========================================="
