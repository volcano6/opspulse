APP_NAME    := opspulse
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE        := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
               -X github.com/volcano6/opspulse/internal/version.Version=$(VERSION) \
               -X github.com/volcano6/opspulse/internal/version.Commit=$(COMMIT) \
               -X github.com/volcano6/opspulse/internal/version.Date=$(DATE)

GOPATH_BIN  := $(shell go env GOPATH 2>/dev/null || echo $(HOME)/go)/bin
export PATH := $(GOPATH_BIN):$(PATH)

.PHONY: build test lint ci clean docker dev tools

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/$(APP_NAME) ./cmd/opspulse

test:
	go test -race -coverprofile=coverage.out ./...

lint:
	golangci-lint run

ci:
	bash scripts/ci.sh

tools:
	go install github.com/mgechev/revive@latest
	go install github.com/kisielk/errcheck@latest
	go install github.com/gordonklaus/ineffassign@latest
	go install github.com/securego/gosec/v2/cmd/gosec@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.5

clean:
	rm -rf bin/ coverage.out

docker:
	docker build -t $(APP_NAME):$(VERSION) .

dev:
	docker compose -f docker-compose.dev.yml up -d
