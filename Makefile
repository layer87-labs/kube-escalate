# =============================================================================
# VARIABLES
# =============================================================================

MODULE      := github.com/layer87-labs/kube-escalate
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT_HASH ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

BUILD_DIR  := build
LOCALBIN   := $(shell pwd)/bin
GOOS       ?= $(shell go env GOOS)
GOARCH     ?= $(shell go env GOARCH)

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commitHash=$(COMMIT_HASH) \
	-X main.buildDate=$(BUILD_DATE)

ENVTEST             ?= $(LOCALBIN)/setup-envtest
ENVTEST_K8S_VERSION := 1.36.x

# Release pipeline variables — set by the CI environment or passed as make args.
BINARY ?= operator
SUFFIX ?=
EXT    ?=

# =============================================================================
# DEFAULT
# =============================================================================

.DEFAULT_GOAL := help

## all: run clean, dep, test, build
.PHONY: all
all: clean dep test build

# =============================================================================
# QUALITY CONTROL
# =============================================================================

## tidy: format code and tidy modfile
.PHONY: tidy
tidy:
	go fmt ./...
	go mod tidy

## audit: vet, staticcheck, govulncheck, golangci-lint, race tests
.PHONY: audit
audit:
	go mod verify
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest -checks=all,-ST1000,-U1000 ./...
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	go run github.com/golangci/golangci-lint/cmd/golangci-lint@latest run ./...
	go test -race -buildvcs -vet=off ./...

## ci/build: cross-compile check for CI matrix (reads GOOS, GOARCH from env)
.PHONY: ci/build
ci/build:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build -trimpath -ldflags "$(LDFLAGS)" ./cmd/operator
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build -trimpath -ldflags "$(LDFLAGS)" ./cmd/kubectl-escalate

# =============================================================================
# DEVELOPMENT
# =============================================================================

## dep: fetch dependencies
.PHONY: dep
dep:
	go mod download

## test: run all tests (downloads envtest binaries on first run)
.PHONY: test
test: envtest
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-path $(LOCALBIN) -p path)" \
		go test -race ./...

## test/cover: run tests with HTML coverage report
.PHONY: test/cover
test/cover: envtest
	mkdir -p $(BUILD_DIR)/coverage
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-path $(LOCALBIN) -p path)" \
		go test -race ./... -coverprofile=$(BUILD_DIR)/coverage/coverage.out
	go tool cover -html=$(BUILD_DIR)/coverage/coverage.out \
		-o $(BUILD_DIR)/coverage/coverage.html
	@echo "Coverage report: $(BUILD_DIR)/coverage/coverage.html"

## test/unit: run unit tests only — no envtest binaries required
.PHONY: test/unit
test/unit:
	go test -race -short ./...

# =============================================================================
# BUILD
# =============================================================================

## build/operator: build the operator binary for the current platform
.PHONY: build/operator
build/operator:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build -trimpath -ldflags "$(LDFLAGS)" \
		-o $(BUILD_DIR)/operator \
		./cmd/operator

## build/plugin: build the kubectl-escalate binary for the current platform
.PHONY: build/plugin
build/plugin:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build -trimpath -ldflags "$(LDFLAGS)" \
		-o $(BUILD_DIR)/kubectl-escalate \
		./cmd/kubectl-escalate

## build: build all binaries for the current platform
.PHONY: build
build: build/operator build/plugin

## build/single: build one release binary for the CI/release pipeline
## Reads BINARY (operator|kubectl-escalate), GOOS, GOARCH, VERSION,
## COMMIT_HASH, BUILD_DATE, SUFFIX, EXT from the environment.
.PHONY: build/single
build/single:
	mkdir -p $(BUILD_DIR)/package
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build \
		-trimpath \
		-ldflags "$(LDFLAGS)" \
		-o "$(BUILD_DIR)/package/$(BINARY)_$(VERSION)_$(SUFFIX)$(EXT)" \
		./cmd/$(BINARY)

## image/build: build container image from pre-built operator binary
##              Run 'make build/operator' (or let CI build it) before this target.
.PHONY: image/build
image/build:
	podman build \
		--platform linux/$(GOARCH) \
		-f deploy/Containerfile \
		-t ghcr.io/layer87-labs/kube-escalate:$(VERSION) \
		$(BUILD_DIR)

## image/push: push the container image to GHCR
.PHONY: image/push
image/push:
	podman push ghcr.io/layer87-labs/kube-escalate:$(VERSION)

# =============================================================================
# TOOLS
# =============================================================================

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## envtest: download setup-envtest to bin/ if not already present
.PHONY: envtest
envtest: $(ENVTEST)
$(ENVTEST): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install \
		sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

## hooks: install git hooks — run once after cloning
.PHONY: hooks
hooks:
	git config core.hooksPath .githooks
	@echo "Git hooks installed — make audit will run before every commit."

# =============================================================================
# CLEAN
# =============================================================================

## clean: remove build artefacts and downloaded tools
.PHONY: clean
clean:
	rm -rf $(BUILD_DIR)/ $(LOCALBIN)/

# =============================================================================
# HELP
# =============================================================================

## help: print this help message
.PHONY: help
help:
	@echo "Usage:"
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/  /'
