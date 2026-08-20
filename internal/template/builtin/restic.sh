#!/bin/bash
# ---
# name: restic
# version: 1
# os: [ubuntu, debian]
# description: Install restic and rclone backup toolchain
# ---
set -euo pipefail

echo "==> Installing restic and rclone..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y restic rclone

echo "==> Verifying restic and rclone installation..."
restic version
rclone version

echo "==> Restic and rclone installed successfully."
