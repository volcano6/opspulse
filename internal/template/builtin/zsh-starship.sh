#!/bin/bash
# ---
# name: zsh-starship
# version: 3
# os: [ubuntu, debian]
# description: Install Zsh + Starship with double-line rounded theme, auto-suggestions and syntax highlighting
# ---
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

if command -v zsh >/dev/null 2>&1 && command -v git >/dev/null 2>&1 && command -v curl >/dev/null 2>&1 && command -v tar >/dev/null 2>&1; then
    echo "==> 1. Basic dependencies (Zsh, Git, Curl, Tar) are already installed. Skipping apt-get."
else
    echo "==> 1. Installing basic dependencies (Zsh, Git, Curl, Tar)..."
    apt-get update -y
    apt-get install -y zsh git curl tar
fi

echo "==> 2. Installing Starship prompt (with mirror fallback)..."
if ! command -v starship &> /dev/null; then
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64)        STARSHIP_ARCH="x86_64-unknown-linux-musl" ;;
        aarch64|arm64) STARSHIP_ARCH="aarch64-unknown-linux-musl" ;;
        *)
            echo "Error: Unsupported architecture for starship: $ARCH" >&2
            exit 1
            ;;
    esac

    TARBALL_NAME="starship-${STARSHIP_ARCH}.tar.gz"
    RAW_URL="https://github.com/starship/starship/releases/latest/download/${TARBALL_NAME}"
    MIRROR_URL="https://ghfast.top/${RAW_URL}"

    TMP_DIR=$(mktemp -d)
    if ! curl -fsSL --connect-timeout 8 -m 30 "$RAW_URL" -o "${TMP_DIR}/${TARBALL_NAME}" 2>/dev/null; then
        echo "--> Direct download timed out, switching to mirror..."
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
TARGET_USER="${SUDO_USER:-$(whoami)}"
TARGET_HOME="$(getent passwd "$TARGET_USER" 2>/dev/null | cut -d: -f6 || echo "$HOME")"
[ -d "$TARGET_HOME" ] || TARGET_HOME="$HOME"

if [ "$TARGET_USER" != "root" ] && [ "$HOME" != "$TARGET_HOME" ]; then
    echo "⚠️  Warning: Current HOME ($HOME) differs from target user's home ($TARGET_HOME). Using $TARGET_HOME for user configuration."
fi
USER_HOME="$TARGET_HOME"

ZSH_PLUGIN_DIR="$USER_HOME/.zsh"
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
mkdir -p "$USER_HOME/.config"
if [ -f "$USER_HOME/.config/starship.toml" ]; then
    echo "==> Backing up existing ~/.config/starship.toml..."
    cp "$USER_HOME/.config/starship.toml" "$USER_HOME/.config/starship.toml.bak.$(date +%Y%m%d%H%M%S)"
fi
cat << 'EOF' > "$USER_HOME/.config/starship.toml"
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

FORCE_EXTRACT="${1:-${SCRIPT_ARG:-}}"

echo "==> 4.5. Extracting user customizations from .bashrc to ~/.zshrc.local..."
should_extract=false
if [ "$FORCE_EXTRACT" = "force" ] || [ "$FORCE_EXTRACT" = "refresh" ]; then
    echo "==> Force extract requested, backing up existing ~/.zshrc.local..."
    [ -f "$USER_HOME/.zshrc.local" ] && cp "$USER_HOME/.zshrc.local" "$USER_HOME/.zshrc.local.bak.$(date +%Y%m%d%H%M%S)"
    should_extract=true
elif [ ! -f "$USER_HOME/.zshrc.local" ] && [ -f "$USER_HOME/.bashrc" ]; then
    should_extract=true
elif [ -f "$USER_HOME/.zshrc.local" ]; then
    if command -v zsh >/dev/null 2>&1 && ! zsh -n "$USER_HOME/.zshrc.local" >/dev/null 2>&1; then
        echo "⚠️  Existing ~/.zshrc.local has syntax errors, repairing..."
        cp "$USER_HOME/.zshrc.local" "$USER_HOME/.zshrc.local.bak.$(date +%Y%m%d%H%M%S)"
        should_extract=true
    else
        echo "ℹ️  ~/.zshrc.local already exists and is valid, skipping extraction (use -t zsh-starship:force to override)."
    fi
fi

if [ "$should_extract" = true ]; then
    TMP_EXTRACT=$(mktemp)
    {
        echo "# Auto-extracted from .bashrc by OpsPulse zsh-starship template"
        echo "# $(date -Iseconds)"
        echo ""

        # 1. Standalone exports (exclude case/if fragments with ';;', broken continuation '\', and bash internal vars)
        grep -E '^\s*export\s+[A-Za-z_][A-Za-z0-9_]*=' "$USER_HOME/.bashrc" 2>/dev/null \
            | grep -v -E ';;|\\$|HISTSIZE|HISTFILESIZE|HISTCONTROL|LESSOPEN|LESSCLOSE|PROMPT_COMMAND|PS1' \
            | sed 's/^\s*//' \
            | awk '!seen[$0]++' || true

        # 2. Standalone aliases (exclude default ls/grep aliases, broken continuation, and case fragments)
        grep -E '^\s*alias\s+[A-Za-z0-9_.-]+=' "$USER_HOME/.bashrc" 2>/dev/null \
            | grep -v -E ';;|\\$|alias\s+(ls|grep|fgrep|egrep|ll|la|l)=' \
            | sed 's/^\s*//' \
            | awk '!seen[$0]++' || true

        # 3. Environment loaders (nvm, cargo, env scripts; avoid bash completion and bashrc loops)
        grep -E '^\s*(\[\s*-[sf]\s+[^]]+\]\s*&&\s*(\\\.|source|\.)|source\s+|\.\s+)' "$USER_HOME/.bashrc" 2>/dev/null \
            | grep -v -E ';;|\\$|bash_completion|bashrc|completion\.bash|completion\s+bash|\.bash_aliases' \
            | sed 's/^\s*//' \
            | awk '!seen[$0]++' || true
    } > "$TMP_EXTRACT"

    # Validate extracted syntax before replacing ~/.zshrc.local
    if command -v zsh >/dev/null 2>&1 && ! zsh -n "$TMP_EXTRACT" >/dev/null 2>&1; then
        echo "⚠️  Extracted customizations failed zsh syntax check, discarding to protect shell..."
        rm -f "$TMP_EXTRACT"
        # If repairing an existing corrupted file, replace with safe placeholder
        if [ -f "$USER_HOME/.zshrc.local" ]; then
            echo "# Cleaned invalid customizations by OpsPulse" > "$USER_HOME/.zshrc.local"
        fi
    elif [ "$(grep -c -v '^[[:space:]]*#\|^[[:space:]]*$' "$TMP_EXTRACT")" -eq 0 ]; then
        rm -f "$TMP_EXTRACT"
        echo "ℹ️  No user customizations found in .bashrc."
        # If repairing an existing corrupted file, ensure the broken file is cleared
        if [ -f "$USER_HOME/.zshrc.local" ]; then
            echo "# No customizations extracted from .bashrc" > "$USER_HOME/.zshrc.local"
        fi
    else
        mv "$TMP_EXTRACT" "$USER_HOME/.zshrc.local"
        echo "✅ User customizations saved to ~/.zshrc.local"
    fi
fi

echo "==> 5. Generating ~/.zshrc..."
if [ -f "$USER_HOME/.zshrc" ]; then
    echo "==> Backing up existing ~/.zshrc..."
    cp "$USER_HOME/.zshrc" "$USER_HOME/.zshrc.bak.$(date +%Y%m%d%H%M%S)"
fi
cat << 'EOF' > "$USER_HOME/.zshrc"
HISTFILE=$HOME/.zsh_history
HISTSIZE=10000
SAVEHIST=10000
setopt appendhistory sharehistory incappendhistory

# Ensure standard user binary directories are in PATH
for dir in "$HOME/.npm-global/bin" "$HOME/.local/bin" "$HOME/bin" "$HOME/go/bin" "/usr/local/go/bin" "$HOME/.cargo/bin"; do
    if [ -d "$dir" ] && [[ ":$PATH:" != *":$dir:"* ]]; then
        export PATH="$dir:$PATH"
    fi
done

alias ll='ls -alF --color=auto'
alias la='ls -A --color=auto'
alias df='df -h'
alias free='free -m'

autoload -Uz compinit && compinit -d ~/.zcompdump
source ~/.zsh/zsh-autosuggestions/zsh-autosuggestions.zsh 2>/dev/null || true
source ~/.zsh/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh 2>/dev/null || true

eval "$(starship init zsh)"

# Node.js toolchains & package managers auto-detection
export NVM_DIR="${NVM_DIR:-$HOME/.nvm}"
if [ -s "$NVM_DIR/nvm.sh" ]; then
    \. "$NVM_DIR/nvm.sh"
elif [ -d "$HOME/.local/share/fnm" ] || command -v fnm >/dev/null 2>&1; then
    [ -d "$HOME/.local/share/fnm" ] && export PATH="$HOME/.local/share/fnm:$PATH"
    eval "$(fnm env 2>/dev/null)"
elif [ -d "$HOME/.volta/bin" ]; then
    export PATH="$HOME/.volta/bin:$PATH"
fi
[ -d "$HOME/.local/share/pnpm" ] && export PATH="$HOME/.local/share/pnpm:$PATH"

# WSL Windows Node.js bridge fallback (prevent permission denied when Linux node is not installed)
if ! command -v node >/dev/null 2>&1 && command -v node.exe >/dev/null 2>&1; then
    alias node='node.exe'
    if command -v npm.cmd >/dev/null 2>&1; then alias npm='npm.cmd'; fi
    if command -v npx.cmd >/dev/null 2>&1; then alias npx='npx.cmd'; fi
fi

# Source user customizations (proxy, API keys, PATH, aliases)
[ -f "$HOME/.zshrc.local" ] && source "$HOME/.zshrc.local"

# Enable OpsPulse completion if available
if command -v ops >/dev/null 2>&1; then
    eval "$(ops completion zsh)"
fi

echo "┌─────────────────────────────────────────────────────────────┐"
echo "│ 🚀 Starship + Zsh modern terminal is ready                  │"
echo "│ • Aliases: ll, la, df, free                                 │"
echo "│ • Auto-suggestions: press [→] key to accept                 │"
echo "└─────────────────────────────────────────────────────────────┘"
EOF

echo "==> 6. Changing default login shell to Zsh..."
ZSH_PATH=$(command -v zsh || which zsh)
chsh -s "$ZSH_PATH" "${SUDO_USER:-$(whoami)}" 2>/dev/null || true

# Ensure correct ownership if executed under sudo
if [ "$TARGET_USER" != "root" ] && [ "$USER_HOME" != "/root" ] && id "$TARGET_USER" &>/dev/null; then
    TARGET_GROUP=$(id -gn "$TARGET_USER" 2>/dev/null || echo "$TARGET_USER")
    chown "$TARGET_USER:$TARGET_GROUP" "$USER_HOME/.zshrc" "$USER_HOME/.zshrc.local" 2>/dev/null || true
    if [ -f "$USER_HOME/.config/starship.toml" ]; then
        chown "$TARGET_USER:$TARGET_GROUP" "$USER_HOME/.config/starship.toml" 2>/dev/null || true
    fi
    case "$USER_HOME/.zsh" in
        "$USER_HOME"/*)
            [ -d "$USER_HOME/.zsh" ] && chown -R "$TARGET_USER:$TARGET_GROUP" "$USER_HOME/.zsh" 2>/dev/null || true
            ;;
    esac
fi

echo "=========================================================="
echo "🎉 Zsh + Starship installation and configuration completed!"
echo "=========================================================="
