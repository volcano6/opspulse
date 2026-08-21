#!/bin/bash
set -euo pipefail

export PATH="$HOME/go/bin:/usr/local/go/bin:$PATH"
cd "$(dirname "$0")/.."

echo "========================================="
echo "   OpsPulse Local CI Verification"
echo "========================================="

echo ""
echo "[1/4] Running go vet..."
go vet ./...
echo "  -> go vet: PASSED"

echo ""
echo "[2/4] Running linters (revive, errcheck, ineffassign, gosec)..."
revive ./...
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
