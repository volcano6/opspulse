#!/bin/bash
# ---
# name: security
# version: 3
# os: [ubuntu, debian]
# description: Basic security hardening (UFW firewall with SSH & Web ports 80/443, fail2ban)
# ---
set -euo pipefail

echo "==> Configuring UFW firewall..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y ufw fail2ban

# Dynamically detect SSH ports from multiple sources:
# 1. Target SSH port passed by OpsPulse executor ($OPS_SSH_PORT)
# 2. Destination port of the active SSH connection ($SSH_CONNECTION)
# 3. Port configured in sshd (sshd -T)
# 4. Port directives in /etc/ssh/sshd_config and /etc/ssh/sshd_config.d/*.conf
# 5. Systemd socket activation port (ssh.socket on Ubuntu 22.10+)
# 6. Active non-loopback listening sockets (ss), strictly excluding 127.0.0.1/::1 (e.g. X11 6010)
detect_ssh_ports() {
    local ports=""

    if [ -n "${OPS_SSH_PORT:-}" ] && [ "${OPS_SSH_PORT}" -gt 0 ] 2>/dev/null; then
        ports="${ports} ${OPS_SSH_PORT}"
    fi

    if [ -n "${SSH_CONNECTION:-}" ]; then
        local cur_port
        cur_port=$(echo "$SSH_CONNECTION" | awk '{print $4}')
        if [ -n "$cur_port" ] && [ "$cur_port" -gt 0 ] 2>/dev/null; then
            ports="${ports} ${cur_port}"
        fi
    fi

    if command -v sshd >/dev/null 2>&1; then
        local sshd_t_ports
        sshd_t_ports=$(sshd -T 2>/dev/null | awk '/^[[:space:]]*port[[:space:]]+/ {print $2}' || true)
        if [ -n "$sshd_t_ports" ]; then
            ports="${ports} ${sshd_t_ports}"
        fi
    fi

    local cfg_ports
    cfg_ports=$(awk '/^[[:space:]]*[Pp]ort[[:space:]]+[0-9]+/ {print $2}' /etc/ssh/sshd_config /etc/ssh/sshd_config.d/*.conf 2>/dev/null || true)
    if [ -n "$cfg_ports" ]; then
        ports="${ports} ${cfg_ports}"
    fi

    if command -v systemctl >/dev/null 2>&1; then
        local socket_ports
        socket_ports=$(systemctl show ssh.socket -p Listen 2>/dev/null | grep -oE ':[0-9]+' | tr -d ':' || true)
        if [ -n "$socket_ports" ]; then
            ports="${ports} ${socket_ports}"
        fi
    fi

    if command -v ss >/dev/null 2>&1; then
        local ss_ports
        ss_ports=$(ss -tlnp 2>/dev/null | grep -E 'sshd|systemd' | awk '$4 !~ /^(127\.|\[::1\])/ {print $4}' | awk -F':' '{print $NF}' || true)
        if [ -n "$ss_ports" ]; then
            ports="${ports} ${ss_ports}"
        fi
    fi

    echo "$ports" | tr ' ' '\n' | grep -E '^[0-9]+$' | awk '$1 >= 1 && $1 <= 65535' | sort -n -u
}

DETECTED_PORTS=$(detect_ssh_ports)

if [ -z "$DETECTED_PORTS" ]; then
    echo "==> [WARN] Could not detect active SSH port, falling back to standard port 22."
    DETECTED_PORTS="22"
fi

for p in $DETECTED_PORTS; do
    echo "==> Allowing detected SSH port: ${p}/tcp"
    ufw allow "${p}/tcp" comment 'SSH'
done

echo "==> Allowing standard Web ports: 80/tcp and 443/tcp..."
ufw allow 80/tcp comment 'HTTP'
ufw allow 443/tcp comment 'HTTPS'

ufw default deny incoming
ufw default allow outgoing
ufw --force enable

echo "==> Enabling Fail2ban..."
systemctl enable --now fail2ban

echo "==> Security hardening completed successfully."
