#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")/.."

HOOK_FILE=".git/hooks/pre-push"
mkdir -p .git/hooks

cat << 'EOF' > "$HOOK_FILE"
#!/bin/bash
# OpsPulse Pre-Push Guard
# Automatically verifies formatting, multi-OS compilation, linters, and unit tests before pushing to remote.

echo "========================================="
echo "   🛡️ Running OpsPulse Pre-Push CI Checks"
echo "========================================="

if command -v wsl.exe &>/dev/null && [ ! -f /.dockerenv ]; then
  wsl.exe -- bash scripts/ci.sh
else
  bash scripts/ci.sh
fi

STATUS=$?
if [ $STATUS -ne 0 ]; then
  echo ""
  echo "❌ Pre-push CI verification failed!"
  echo "Push aborted to prevent broken builds on GitHub Actions."
  echo "Fix the issues reported above, or run 'git push --no-verify' to bypass in emergency."
  exit $STATUS
fi

echo "✅ Pre-push CI verification passed. Proceeding with push..."
exit 0
EOF

chmod +x "$HOOK_FILE" 2>/dev/null || true
echo "✅ Git pre-push hook installed successfully at $HOOK_FILE"
