#!/bin/bash
# ---
# name: zsh-starship
# version: 1
# os: [ubuntu, debian]
# description: Install Zsh + Starship with double-line rounded theme, auto-suggestions and syntax highlighting
# ---
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

echo "==> 1. Installing basic dependencies (Zsh, Git, Curl, Tar)..."
apt-get update -y
apt-get install -y zsh git curl tar

echo "==> 2. Installing Starship prompt (with mirror fallback)..."
if ! command -v starship &> /dev/null; then
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64)  STARSHIP_ARCH="x86_64-unknown-linux-musl" ;;
        aarch64) STARSHIP_ARCH="aarch64-unknown-linux-musl" ;;
        *)       STARSHIP_ARCH="x86_64-unknown-linux-musl" ;;
    esac

    TARBALL_NAME="starship-${STARSHIP_ARCH}.tar.gz"
    RAW_URL="https://github.com/starship/starship/releases/latest/download/${TARBALL_NAME}"
    MIRROR_URL="https://ghfast.top/${RAW_URL}"

    TMP_DIR=$(mktemp -d)
    if ! curl -fsSL --connect-timeout 8 -m 30 "$RAW_URL" -o "${TMP_DIR}/${TARBALL_NAME}" 2>/dev/null; then
        echo "--> Direct download timed out, switching to accelerated mirror..."
        curl -fsSL --connect-timeout 10 -m 60 "$MIRROR_URL" -o "${TMP_DIR}/${TARBALL_NAME}"
    fi

    tar -xzf "${TMP_DIR}/${TARBALL_NAME}" -C /usr/local/bin
    chmod +x /usr/local/bin/starship
    rm -rf "$TMP_DIR"
    echo "✅ Starship installed successfully."
else
    echo "ℹ️ Starship already installed."
fi

echo "==> 3. Installing high-frequency plugins (autosuggestions + syntax-highlighting)..."
ZSH_PLUGIN_DIR="$HOME/.zsh"
mkdir -p "$ZSH_PLUGIN_DIR"

clone_or_mirror() {
    local target_dir="$1"
    local repo_url="$2"
    if [ ! -d "$target_dir" ]; then
        if ! git clone --depth=1 --timeout=15 "$repo_url" "$target_dir" 2>/dev/null; then
            echo "--> Clone timed out, switching to mirror: $target_dir..."
            git clone --depth=1 "https://ghfast.top/${repo_url}" "$target_dir"
        fi
    fi
}

clone_or_mirror "$ZSH_PLUGIN_DIR/zsh-autosuggestions" "https://github.com/zsh-users/zsh-autosuggestions"
clone_or_mirror "$ZSH_PLUGIN_DIR/zsh-syntax-highlighting" "https://github.com/zsh-users/zsh-syntax-highlighting"

echo "==> 4. Generating Starship theme config (~/.config/starship.toml)..."
mkdir -p "$HOME/.config"
cat << 'EOF' > "$HOME/.config/starship.toml"
format = """
$username\
$hostname\
$directory\
$git_branch\
$git_status\
$cmd_duration\
$line_break\
$character"""

[character]
success_symbol = "[╰─❯](<bold #50fa7b>)"
error_symbol = "[╰─❯](<bold #ff5555>)"

[directory]
format = "[╭─](<bold #8be9fd>) [$path]($style) "
style = "bold #8be9fd"
truncation_length = 3
truncate_to_repo = true

[git_branch]
format = "on [$symbol$branch]($style) "
symbol = "git:"
style = "bold #f1fa8c"

[git_status]
format = "([$all_status$ahead_behind]($style) )"
style = "bold #ff5555"

[cmd_duration]
min_time = 2000
format = "took [$duration]($style) "
style = "bold #ffb86c"

[docker_context]
disabled = true

[hostname]
disabled = true

[username]
disabled = true
EOF

echo "==> 5. Generating ~/.zshrc..."
cat << 'EOF' > "$HOME/.zshrc"
HISTFILE=$HOME/.zsh_history
HISTSIZE=10000
SAVEHIST=10000
setopt appendhistory sharehistory incappendhistory

alias ll='ls -alF --color=auto'
alias la='ls -A --color=auto'
alias df='df -h'
alias free='free -m'

autoload -Uz compinit && compinit -d ~/.zcompdump
source ~/.zsh/zsh-autosuggestions/zsh-autosuggestions.zsh 2>/dev/null || true
source ~/.zsh/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh 2>/dev/null || true

eval "$(starship init zsh)"

echo "┌─────────────────────────────────────────────────────────────┐"
echo "│ 🚀 Starship + Zsh modern terminal is ready                  │"
echo "│ • Aliases: ll, la, df, free                                 │"
echo "│ • Auto-suggestions: press [→] key to accept                 │"
echo "└─────────────────────────────────────────────────────────────┘"
EOF

echo "==> 6. Changing default login shell to Zsh..."
ZSH_PATH=$(which zsh)
chsh -s "$ZSH_PATH" "$(whoami)" 2>/dev/null || true

echo "=========================================================="
echo "🎉 Zsh + Starship installation and configuration completed!"
echo "=========================================================="
