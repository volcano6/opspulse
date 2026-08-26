#!/bin/bash
# ---
# name: timezone
# version: 1
# os: [ubuntu, debian]
# description: Set system timezone (default Asia/Shanghai, e.g. -t timezone:UTC) and enable NTP
# ---
set -euo pipefail

TARGET_TZ="${1:-Asia/Shanghai}"

echo "==> Configuring system timezone to '${TARGET_TZ}'..."
if [ -f "/usr/share/zoneinfo/${TARGET_TZ}" ]; then
    ln -sf "/usr/share/zoneinfo/${TARGET_TZ}" /etc/localtime
    echo "${TARGET_TZ}" > /etc/timezone
elif command -v timedatectl >/dev/null 2>&1; then
    timedatectl set-timezone "${TARGET_TZ}" || true
else
    echo "⚠️ Warning: Timezone '${TARGET_TZ}' not found in /usr/share/zoneinfo. Using default."
fi

echo "==> Enabling network time synchronization (NTP)..."
if command -v timedatectl >/dev/null 2>&1; then
    timedatectl set-ntp true 2>/dev/null || true
fi

# Ensure systemd-timesyncd is enabled if installed
if systemctl list-unit-files | grep -q systemd-timesyncd; then
    systemctl enable --now systemd-timesyncd 2>/dev/null || true
fi

echo "==> Current date and time:"
date -R
echo "==> Timezone configured successfully."
