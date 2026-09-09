#!/bin/bash
# ---
# name: upgrade
# version: 2
# os: [ubuntu, debian]
# description: Unattended system security updates and package upgrades
# ---
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
echo "==> Updating package indices and applying security upgrades..."
apt-get update -y
apt-get upgrade -y
apt-get autoremove -y

if [ -f /var/run/reboot-required ]; then
    echo "⚠️ [NOTICE] System reboot is required to apply kernel or core library updates."
    if [ -f /var/run/reboot-required.pkgs ]; then
        echo "Packages requiring reboot:"
        cat /var/run/reboot-required.pkgs
    fi
fi

echo "=========================================================="
echo "✅ System packages upgraded successfully."
echo "=========================================================="
