# OpsPulse Makefile —— 构建与校验的唯一入口。
#
# 这里每个目标都是「可被 CI 直接调用的原子闸门」：.github/workflows/ci.yaml 的
# 每个 job 只调用一个目标，scripts/ci.sh 只是 `make ci` 的薄包装。所以
# 「本地 `make ci` 通过 ⇔ CI 通过」成立——历史上 CI 与 ci.sh 各写一套命令，
# 已经漂移过（govet enable-all vs 默认、_test.go 的 errcheck/gosec 豁免不对称）。
#
# 工具版本只在本文件出现一次：golangci-lint 与 govulncheck 都用
# `go run <pkg>@<pin>` 按需取用，不依赖 PATH 上已安装的二进制——否则同一份代码
# 会因本机残留的旧版本跑出不同结果（旧 ci.sh 的五个 linter 全部 @latest）。
APP_NAME    := ops
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE        := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_PKG := github.com/volcano6/opspulse/internal/version
LDFLAGS     := -s -w \
               -X $(VERSION_PKG).Version=$(VERSION) \
               -X $(VERSION_PKG).Commit=$(COMMIT) \
               -X $(VERSION_PKG).Date=$(DATE)

GOPATH_BIN  := $(shell go env GOPATH 2>/dev/null || echo $(HOME)/go)/bin
LOCAL_BIN   := $(HOME)/.local/bin

# 工具 pin（唯一事实源）。升级时只改这里，CI 与本地同时生效。
GOLANGCI_LINT_VERSION ?= v1.64.5
GOVULNCHECK_VERSION   ?= v1.1.4
GOLANGCI_LINT         := go run github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOVULNCHECK           := go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

# Proxy the container build uses to fetch modules. Defaults to whatever this
# machine's Go is configured with (`go env GOPROXY`), because that is the value
# known to work on this network: a machine behind a regional mirror would
# otherwise build fine locally and fail inside the container. Override with
# `make docker GO_PROXY=...`.
GO_PROXY ?= $(shell go env GOPROXY)

# 覆盖率阈值（百分数）。实测总覆盖率（`go test -race -coverprofile=coverage.out ./...`，
# 含 scripts/opstub 这个 0 覆盖的调试工具包）在 66.6%–67.7% 之间波动，按最低值向下取整到
# 5 的倍数 = 65。需要临时放行时：COVER_MIN=0 make cover
COVER_FILE ?= coverage.out
COVER_MIN  ?= 65

.PHONY: all build build-cross install fmt-check vet vet-cross test cover vuln lint e2e neutrality docker ci clean

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP_NAME) ./cmd/opspulse
	@cp bin/$(APP_NAME) bin/opspulse 2>/dev/null || true

# 三个发布平台的交叉编译。只做编译校验 + 留出可用产物；真正的六平台矩阵在 release.yaml。
build-cross:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP_NAME)-linux-amd64 ./cmd/opspulse
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP_NAME)-darwin-amd64 ./cmd/opspulse
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP_NAME)-windows-amd64.exe ./cmd/opspulse
	@echo "build-cross: linux/darwin/windows 构建通过"

install: build
	@mkdir -p $(GOPATH_BIN)
	@cp --remove-destination bin/$(APP_NAME) $(GOPATH_BIN)/$(APP_NAME) 2>/dev/null || cp bin/$(APP_NAME) $(GOPATH_BIN)/$(APP_NAME)
	@cp --remove-destination bin/$(APP_NAME) $(GOPATH_BIN)/opspulse 2>/dev/null || true
	@mkdir -p $(LOCAL_BIN) 2>/dev/null || true
	@cp --remove-destination bin/$(APP_NAME) $(LOCAL_BIN)/$(APP_NAME) 2>/dev/null || cp bin/$(APP_NAME) $(LOCAL_BIN)/$(APP_NAME)
	@cp --remove-destination bin/$(APP_NAME) $(LOCAL_BIN)/opspulse 2>/dev/null || true
	@echo "✅ Installed $(APP_NAME) to $(GOPATH_BIN)/$(APP_NAME) and $(LOCAL_BIN)/$(APP_NAME)"
	@case ":$$PATH:" in \
		*":$(GOPATH_BIN):"*|*":$(LOCAL_BIN):"*) ;; \
		*) \
			echo "💡 Tip: Neither $(GOPATH_BIN) nor $(LOCAL_BIN) is in your current PATH."; \
			echo "   Run: ./bin/$(APP_NAME) completion --install"; \
			echo "   Or add to your shell profile: export PATH=\"\$$HOME/.local/bin:\$$HOME/go/bin:\$$PATH\"" ;; \
	esac

# 已跟踪的 Go 文件必须 gofmt-clean（golangci-lint 的 gofmt linter 是第二道，但它的
# 报错只到文件级，这里给出可直接 `gofmt -w` 的清单）。
fmt-check:
	@unformatted="$$(gofmt -l $$(go list -f '{{.Dir}}' ./...))"; \
	if [ -n "$$unformatted" ]; then \
		echo "fmt-check: 以下文件未通过 gofmt（gofmt -w <file> 修复）：" >&2; \
		echo "$$unformatted" >&2; \
		exit 1; \
	fi; \
	echo "fmt-check: 全部 Go 源文件已 gofmt 格式化"

vet:
	go vet ./...

# 跨平台 vet：Windows/macOS 专属文件（ssh_windows.go、lock_unix.go …）在 linux 上被
# build tag 排除，只有换 GOOS 才会被类型检查到。
vet-cross:
	@for os in linux darwin windows; do \
		echo "vet-cross: GOOS=$$os"; \
		GOOS=$$os go vet ./... || exit 1; \
	done
	@echo "vet-cross: linux/darwin/windows vet 通过"

# 全量单元测试（-race），同时产出覆盖率 profile 供 cover 目标复用（因此 `make ci`
# 只需要跑一遍测试）。
test:
	go test -race -coverprofile=$(COVER_FILE) ./...

# 覆盖率阈值门禁：复用 test 产出的 profile（单独调用时会先跑 test）。
cover: test
	@total="$$(go tool cover -func=$(COVER_FILE) | awk '$$1 == "total:" { sub(/%/, "", $$3); print $$3 }')"; \
	if [ -z "$$total" ]; then \
		echo "cover: 无法从 $(COVER_FILE) 读取总覆盖率（文件缺失或格式异常）" >&2; \
		exit 1; \
	fi; \
	awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { \
		if (t + 0 < m + 0) { \
			printf "cover: 总覆盖率 %s%% 低于阈值 %s%%\n", t, m; \
			exit 1; \
		} \
		printf "cover: 总覆盖率 %s%% ≥ 阈值 %s%%\n", t, m; \
	}'

# 依赖漏洞扫描。发现漏洞即失败（阈值/忽略清单不在这里放宽——真要忽略就显式写进
# govulncheck 的调用参数，并在这里留一行说明原因）。
vuln:
	$(GOVULNCHECK) ./...

# 唯一的 lint 实现（.golangci.yml）。历史教训：CI 跑 golangci-lint、ci.sh 跑
# revive/errcheck/ineffassign/gosec/staticcheck 五个独立工具，两边的检查集永远对不齐。
lint:
	$(GOLANGCI_LINT) run

# 1Password 备份/还原的离线端到端（stub op CLI）：真实 `op` 需要桌面端交互授权，
# 无法在 CI 里驱动，这条路径是唯一能覆盖它的自动化。
e2e:
	bash scripts/verify-onepassword.sh

# 中性化门禁：已跟踪文件不得出现真实地址/域名/家目录/私钥/库名。
neutrality:
	bash scripts/check-neutrality.sh

# 镜像可构建性 + 版本注入（与 release 的 ldflags 对齐，避免镜像自报 dev/none/unknown）。
docker:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		--build-arg 'GOPROXY=$(GO_PROXY)' \
		-t $(APP_NAME):$(VERSION) .
	@out="$$(docker run --rm $(APP_NAME):$(VERSION) version)"; \
	echo "$$out"; \
	case "$$out" in \
		*"$(COMMIT)"*) echo "docker: 版本注入已生效（COMMIT=$(COMMIT)）" ;; \
		*) echo "docker: 镜像里的版本信息缺少 COMMIT=$(COMMIT)，检查 make docker 的 --build-arg" >&2; exit 1 ;; \
	esac

# 完整的本地门禁：与 CI 的 job 集合一一对应（见 .github/workflows/ci.yaml）。
# cover 依赖 test，make 同一次调用不会重复执行 test。
ci: fmt-check vet vet-cross test cover vuln lint build-cross e2e neutrality docker

clean:
	rm -rf bin/ dist/ $(COVER_FILE)
