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
echo "   OpsPulse Local CI Verification"
echo "========================================="

echo ""
echo "[1/4] Running go vet..."
go vet ./...
echo "  -> go vet: PASSED"

echo ""
echo "[2/4] Running linters (revive, errcheck, ineffassign, gosec)..."
check_tool "revive" "github.com/mgechev/revive@latest"
check_tool "errcheck" "github.com/kisielk/errcheck@latest"
check_tool "ineffassign" "github.com/gordonklaus/ineffassign@latest"
check_tool "gosec" "github.com/securego/gosec/v2/cmd/gosec@latest"

revive -set_exit_status ./...
errcheck ./...
ineffassign ./...
gosec -quiet -exclude=G106,G204,G304 ./...
echo "  -> Linters: PASSED"

echo ""
echo "[3/4] Running unit tests with race detector..."
go test -race -coverprofile=coverage.out ./...
echo "  -> Unit tests: PASSED"

echo ""
echo "[4/4] Building binary (CGO_ENABLED=0)..."
CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/opspulse ./cmd/opspulse
./bin/opspulse version
echo "  -> Build: PASSED"

echo ""
echo "========================================="
echo "   ✅ ALL LOCAL CI CHECKS PASSED!"
echo "========================================="
