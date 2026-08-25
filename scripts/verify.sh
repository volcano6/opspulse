#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="/usr/local/go/bin:$PATH"

echo "=== GO VERSION ==="
go version

echo "=== MOD TIDY ==="
go mod tidy

echo "=== BUILD ==="
CGO_ENABLED=0 go build -o bin/opspulse ./cmd/opspulse

echo "=== VERSION ==="
./bin/opspulse version

echo "=== TEST ==="
go test ./...

echo "=== DONE ==="
