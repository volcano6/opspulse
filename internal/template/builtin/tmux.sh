#!/bin/bash
# ---
# name: tmux
# version: 1
# os: [ubuntu, debian]
# description: Install tmux with mouse scroll support, 10000-line history, and clean status bar
# ---
set -euo pipefail

echo "==> Installing tmux..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y tmux

echo "==> Configuring global /etc/tmux.conf..."
cat << 'EOF' > /etc/tmux.conf
# Enable full mouse support (click, resize, scroll)
set -g mouse on

# Increase history scrollback buffer to 10,000 lines
set -g history-limit 10000

# True color / 256 color terminal support
set -g default-terminal "screen-256color"
set -ga terminal-overrides ",*256col*:Tc"

# Start window and pane indices at 1 instead of 0
set -g base-index 1
setw -g pane-base-index 1

# Intuitive split window shortcuts (| for horizontal, - for vertical)
bind-key | split-window -h -c "#{pane_current_path}"
bind-key - split-window -v -c "#{pane_current_path}"

# Fast reload shortcut
bind-key r source-file /etc/tmux.conf \; display-message "tmux.conf reloaded!"

# Clean, elegant dark status bar
set -g status-style bg='#282a36',fg='#f8f8f2'
set -g status-left-length 30
set -g status-left '#[bg=#6272a4,fg=#f8f8f2,bold] #S #[bg=default,fg=default] '
set -g status-right '#[fg=#8be9fd]%Y-%m-%d %H:%M #[bg=#50fa7b,fg=#282a36,bold] #H '
setw -g window-status-current-style bg='#bd93f9',fg='#282a36',bold
setw -g window-status-current-format ' #I:#W '
setw -g window-status-format ' #I:#W '
EOF

echo "==> tmux installed and configured successfully."
