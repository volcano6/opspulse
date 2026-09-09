#!/bin/bash
# ---
# name: cluster-check
# version: 1
# os: [ubuntu, debian]
# description: Inspect server hostname, ip, load, memory, and disk usage
# ---
set -euo pipefail

NODE_NAME="$(hostname 2>/dev/null || echo 'Unknown')"
UPTIME_STR="$(uptime 2>/dev/null || echo 'N/A')"
IP_STR="$(hostname -I 2>/dev/null | awk '{print $1}' || echo 'N/A')"
MEM_STR="$(free -h 2>/dev/null | awk '/Mem:/ {print $3 "/" $2}' || echo 'N/A')"
DISK_STR="$(df -h / 2>/dev/null | awk 'NR==2 {print $4 " free (total " $2 ")"}' || echo 'N/A')"
BBR_STR="$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo 'N/A')"

echo "==================== Node Status [${NODE_NAME}] ===================="
echo "System Uptime/Load: ${UPTIME_STR}"
echo "Primary IP Address: ${IP_STR}"
echo "Memory Usage      : ${MEM_STR}"
echo "Root Disk Space   : ${DISK_STR}"
echo "TCP Congestion    : ${BBR_STR}"
echo "====================================================================="
