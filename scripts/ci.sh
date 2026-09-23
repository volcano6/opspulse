#!/bin/bash
set -euo pipefail

GOPATH_BIN="$(go env GOPATH 2>/dev/null || echo "$HOME/go")/bin"
export PATH="$GOPATH_BIN:$HOME/go/bin:/usr/local/go/bin:$PATH"
cd "$(dirname "$0")/.."

check_tool() {
  local tool="$1"
  local pkg="$2"
  if ! command -v "$tool" &>/dev/null; then
    echo "  [+] Tool '$tool' not found, installing '$pkg'..."
    go install "$pkg"
  fi
}

echo "========================================="
echo "   OpsPulse Deep CI Verification"
echo "========================================="

echo ""
echo ""
echo "[1/6] Checking tracked files for non-neutralized real values..."
bash scripts/check-neutrality.sh
echo "  -> Neutrality: PASSED"
echo ""
echo "[2/6] Running Multi-OS Cross-Compilation Vet (Linux, Windows, macOS)..."
echo "  -> Checking GOOS=linux..."
GOOS=linux go vet ./...
echo "  -> Checking GOOS=windows..."
GOOS=windows go vet ./...
echo "  -> Checking GOOS=darwin..."
GOOS=darwin go vet ./...
echo "  -> Multi-OS Vet: PASSED"

echo ""
echo "[3/6] Running Linters (gofmt, revive, errcheck, ineffassign, gosec, staticcheck)..."
check_tool "revive" "github.com/mgechev/revive@latest"
check_tool "errcheck" "github.com/kisielk/errcheck@latest"
check_tool "ineffassign" "github.com/gordonklaus/ineffassign@latest"
check_tool "gosec" "github.com/securego/gosec/v2/cmd/gosec@latest"
check_tool "staticcheck" "honnef.co/go/tools/cmd/staticcheck@latest"

echo "  -> gofmt..."
unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
  echo "  ✗ The following files are not gofmt-formatted:" >&2
  echo "$unformatted" >&2
  exit 1
fi
echo "  -> revive..."
revive -set_exit_status ./...
echo "  -> errcheck..."
errcheck ./...
echo "  -> ineffassign..."
ineffassign ./...
echo "  -> gosec..."
gosec -quiet -exclude=G106,G204,G304,G703 ./...
echo "  -> staticcheck (unused, style, static analysis)..."
staticcheck ./...
echo "  -> Linters: PASSED"

echo ""
echo "[4/6] Running unit tests with race detector..."
go test -race -coverprofile=coverage.out ./...
echo "  -> Unit tests: PASSED"

echo ""
echo "[5/6] Building static binaries for Linux & Windows..."
mkdir -p bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o bin/ops ./cmd/opspulse
cp bin/ops bin/opspulse 2>/dev/null || true
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" -o bin/ops.exe ./cmd/opspulse
./bin/ops version
echo "  -> Build: PASSED"

echo ""
echo "[6/6] Running 1Password end-to-end verification (stub CLI)..."
# The 1Password flows cannot be unit-tested: the real `op` needs an interactive
# Desktop App approval, so scripts/verify-onepassword.sh drives a stub CLI and
# asserts on the exact call sequence. Without this step the harness only ever ran
# when someone remembered to invoke it by hand.
bash scripts/verify-onepassword.sh
echo "  -> 1Password E2E: PASSED"

echo ""
echo "========================================="
echo "   ✅ ALL DEEP CI CHECKS PASSED!"
echo "========================================="
