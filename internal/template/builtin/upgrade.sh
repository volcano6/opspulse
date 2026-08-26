#!/bin/bash
# ---
# name: upgrade
# version: 1
# os: [ubuntu, debian]
# description: Unattended system security updates and package upgrades
# ---
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
echo "==> Updating package indices and applying security upgrades..."
apt-get update -y
apt-get upgrade -y
apt-get autoremove -y

echo "=========================================================="
echo "✅ System packages upgraded successfully."
echo "=========================================================="
