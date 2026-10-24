# qStack — build and verification entry points.
#
# Runs on Linux: the DevSpace dev pod and CI. On a Windows workstation run
# targets inside the pod: `devspace run verify` (see devspace.yaml).

SHELL := bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help
MAKEFLAGS += --no-print-directory

# Keep in sync with the `toolchain` directive in go.mod (tools/go.mod needs >= 1.26.7).
export GOTOOLCHAIN := go1.26.10

GO          := go
TOOL        := $(GO) tool -modfile=tools/go.mod
BUF         := $(TOOL) buf
GOLANGCI    := $(TOOL) golangci-lint
SQLC        := $(TOOL) sqlc
# Helm lives in its own module (Kubernetes client libraries).
HELM        := $(GO) tool -modfile=tools/helm/go.mod helm
CHART       := deploy/helm/qstack-control-plane

PROTO_DIR   := api/proto
GEN_DIR     := gen
PROTO_FILES := $(shell find $(PROTO_DIR) -name '*.proto' 2>/dev/null)
# Each service owns its schema: internal/<svc>/store/sqlc.yaml.
SQLC_CONFIGS := $(shell find internal -path '*/store/sqlc.yaml' 2>/dev/null)

.PHONY: help
help: ## Show available targets
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-18s %s\n", $$1, $$2}'

##@ Code generation

.PHONY: proto
proto: ## buf lint + breaking (vs main) + generate
ifeq ($(strip $(PROTO_FILES)),)
	@echo "proto: no .proto files under $(PROTO_DIR), skipping"
else
	$(BUF) format -d --exit-code $(PROTO_DIR)
	$(BUF) lint
	@if [ -d .git ] && git cat-file -e main:$(PROTO_DIR) 2>/dev/null; then \
		$(BUF) breaking --against '.git#branch=main'; \
	else \
		echo "proto: breaking check skipped (no .git or no protos on main; CI runs it)"; \
	fi
	@# Only our own protos; vendored third_party/proto keeps its upstream Go packages.
	$(BUF) generate --path $(PROTO_DIR)/qstack
endif

.PHONY: sqlc
sqlc: ## Generate store code from SQL (per-service sqlc.yaml)
ifeq ($(strip $(SQLC_CONFIGS)),)
	@echo "sqlc: no internal/*/store/sqlc.yaml, skipping"
else
	@for cfg in $(SQLC_CONFIGS); do echo "sqlc: $$cfg"; $(SQLC) generate -f "$$cfg"; done
endif

.PHONY: gen
gen: proto sqlc ## All code generation

##@ Checks

.PHONY: vet
vet: ## go vet
	$(GO) vet ./...

.PHONY: lint
lint: ## golangci-lint
	$(GOLANGCI) run ./...

.PHONY: test
test: ## Unit tests (race detector)
	$(GO) test -race -count=1 ./...

.PHONY: test-integration
test-integration: ## Integration tests (build tag `integration`; PostgreSQL/NATS from env or testcontainers)
	$(GO) test -race -count=1 -tags=integration ./...

.PHONY: licenses
licenses: ## Fail on dependency licenses incompatible with Apache-2.0 (GPL/AGPL/unknown)
	$(TOOL) go-licenses check ./... --disallowed_types=forbidden,restricted,unknown

.PHONY: tidy-check
tidy-check: ## Fail if go.mod/go.sum are not tidy (does not modify files)
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	for f in go.mod go.sum; do [ -f $$f ] && cp $$f "$$tmp/"; done; \
	$(GO) mod tidy; \
	dirty=0; \
	for f in go.mod go.sum; do \
		if [ -f "$$tmp/$$f" ] || [ -f $$f ]; then \
			cmp -s "$$tmp/$$f" $$f 2>/dev/null || dirty=1; \
		fi; \
	done; \
	for f in go.mod go.sum; do \
		if [ -f "$$tmp/$$f" ]; then cp "$$tmp/$$f" $$f; else rm -f $$f; fi; \
	done; \
	if [ $$dirty -eq 1 ]; then echo "tidy-check: go.mod/go.sum are not tidy, run 'go mod tidy'"; exit 1; fi; \
	echo "tidy-check: ok"

.PHONY: gen-check
gen-check: ## Fail if `make gen` changes generated code (works without .git)
	@hash() { \
		local dirs=(); for d in $(GEN_DIR) internal; do [ -d "$$d" ] && dirs+=("$$d"); done; \
		if [ $${#dirs[@]} -eq 0 ]; then echo none; return; fi; \
		find "$${dirs[@]}" -type f \( -path '$(GEN_DIR)/*' -o -name '*_sqlc.go' -o -name '*.sql.go' \) -print0 | sort -z | xargs -0 -r sha256sum | sha256sum; \
	}; \
	before=$$(hash); \
	$(MAKE) gen; \
	after=$$(hash); \
	if [ "$$before" != "$$after" ]; then echo "gen-check: generated code is out of date, run 'make gen' and commit"; exit 1; fi; \
	echo "gen-check: ok"

.PHONY: chart-lint
chart-lint: ## helm lint + render the control-plane chart (dev values; production values must refuse dev-only parts)
	$(HELM) lint $(CHART) -f $(CHART)/values-dev.yaml --strict
	$(HELM) template qstack $(CHART) -n qstack-l0 -f $(CHART)/values-dev.yaml >/dev/null
	@if $(HELM) template qstack $(CHART) >/dev/null 2>&1; then \
		echo "chart-lint: default (production) values must not render OpenBao dev mode"; exit 1; fi
	@echo "chart-lint: ok"

.PHONY: verify
verify: tidy-check gen-check vet lint licenses chart-lint test test-integration ## Everything CI runs
	@echo "verify: ok"

##@ Build and packaging

VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
ARCH        ?= amd64
# Package versions must start with a digit (Debian policy).
DEB_VERSION ?= $(if $(filter v%,$(VERSION)),$(patsubst v%,%,$(VERSION)),0.0.0~$(VERSION))
LDFLAGS     := -s -w -X github.com/mindevis/qstack/pkg/version.Version=$(VERSION) -X github.com/mindevis/qstack/pkg/version.Commit=$(COMMIT)
BINARIES    := $(notdir $(wildcard cmd/*))
# Host binaries shipped as .deb (the rest are OCI images of the control plane).
DEB_BINARIES := qs-node
IMAGE_REPO  ?= localhost:5000/qstack
OCI_BUILDER ?= docker

.PHONY: build
build: ## Build static linux binaries into bin/linux-$(ARCH)/
	@for b in $(BINARIES); do \
		echo "build: $$b"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/linux-$(ARCH)/$$b ./cmd/$$b; \
	done

.PHONY: deb
deb: build ## Build .deb packages of host binaries into dist/
	@mkdir -p dist bin/deb
	@for b in $(DEB_BINARIES); do \
		cp bin/linux-$(ARCH)/$$b bin/deb/$$b; \
		VERSION=$(DEB_VERSION) ARCH=$(ARCH) $(TOOL) nfpm package -f deploy/nfpm/$$b.yaml -p deb -t dist/; \
	done

.PHONY: image
image: ## Build OCI images (distroless, Debian 13): BIN=<binary> (default: all)
	@for b in $(or $(BIN),$(BINARIES)); do \
		$(OCI_BUILDER) build -f deploy/oci/Dockerfile --build-arg BINARY=$$b \
			--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
			-t $(IMAGE_REPO)/$$b:$(VERSION) .; \
	done

##@ Web console (console/, ADR-0017)

PNPM := npx -y pnpm@12.14.0

.PHONY: console-build
console-build: ## Install and build the console (Nuxt SSR) into console/.output
	cd console && $(PNPM) install --frozen-lockfile && $(PNPM) typecheck && $(PNPM) build

.PHONY: console-image
console-image: ## Build the qs-console OCI image
	$(OCI_BUILDER) build -t $(IMAGE_REPO)/qs-console:$(VERSION) --build-arg VERSION=$(VERSION) console

##@ Environments

# L0 stand (spec 13 §1.1): needs kubectl access to L0_NAMESPACE and the
# CloudNativePG operator. On the DevSpace stand: `devspace run l0-up`.
L0_NAMESPACE ?= qstack-l0
L0_ENV := L0_NAMESPACE=$(L0_NAMESPACE) HELM="$(HELM)"

.PHONY: dev-up
dev-up: ## Deploy the L0 stand (L0_IMAGE_REGISTRY, L0_IMAGE_TAG, L0_MAX_SECONDS)
	$(L0_ENV) deploy/l0/up.sh

.PHONY: dev-smoke
dev-smoke: ## Check a running L0 stand
	$(L0_ENV) deploy/l0/smoke.sh

.PHONY: dev-down
dev-down: ## Remove the L0 stand and its volumes
	$(L0_ENV) deploy/l0/down.sh

.PHONY: e2e
e2e:
	@echo "e2e: no scenarios yet (first services: phase 1)"; exit 1
