# Go project root autodetection
BACKEND_DIR := $(shell \
  if [ -f backend/go.mod ]; then echo backend; \
  elif [ -f go.mod ]; then echo .; \
  else echo ""; fi )
ifeq ($(BACKEND_DIR),)
$(error Could not find go.mod in backend/ or project root)
endif

GO_INSTALL_DIR := $(HOME)/.go
GOLANGCI_LINT_OPTS ?= --modules-download-mode=mod
AGENT_PKG    := ./cmd/go-monitoring
BUILD_OUTPUT  = $(CURDIR)/go-monitoring
VERSION_PKG  := github.com/mordilloSan/go-monitoring/internal/version
GIT_VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "untracked")
GIT_COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "")
BUILD_TIME   ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS      := -w -s \
  -X $(VERSION_PKG).Version=$(GIT_VERSION) \
  -X $(VERSION_PKG).CommitSHA=$(GIT_COMMIT) \
  -X $(VERSION_PKG).BuildTime=$(BUILD_TIME)

GO_BIN := $(or $(wildcard $(GO_INSTALL_DIR)/bin/go),$(shell command -v go 2>/dev/null))
ifeq ($(GO_BIN),)
$(error Could not find go in $(GO_INSTALL_DIR)/bin or PATH)
endif
GOFMT := $(or $(wildcard $(dir $(GO_BIN))gofmt),$(shell command -v gofmt 2>/dev/null),gofmt)
GO_TOOLCHAIN ?= auto
GO_CMD_ENV = env PATH="$(GO_INSTALL_DIR)/bin:$$PATH" GOTOOLCHAIN=$(GO_TOOLCHAIN)
GOLANGCI_LINT := $(or $(wildcard $(GO_INSTALL_DIR)/bin/golangci-lint),$(shell command -v golangci-lint 2>/dev/null),golangci-lint)
MODERNIZE_MODULE      := golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize
MODERNIZE_VERSION     ?= latest

# Default OS/ARCH values
OS ?= $(shell $(GO_CMD_ENV) "$(GO_BIN)" env GOOS 2>/dev/null)
ARCH ?= $(shell $(GO_CMD_ENV) "$(GO_BIN)" env GOARCH 2>/dev/null)
# GOAMD64 controls x86-64 microarchitecture tuning for amd64 builds.
# v3 targets x86-64-v3 CPUs; set GOAMD64=v1 for maximum amd64 compatibility.
GOAMD64 ?= v3
GOAMD64_ENV :=
ifeq ($(ARCH),amd64)
GOAMD64_ENV := GOAMD64=$(GOAMD64)
endif
# Controls NVML/glibc agent build tag behavior:
# - auto (default): enable on linux/amd64 glibc hosts
# - true: always enable
# - false: always disable
NVML ?= auto

# Detect glibc host for local linux/amd64 builds.
HOST_GLIBC := $(shell \
	if [ "$(OS)" = "linux" ] && [ "$(ARCH)" = "amd64" ]; then \
		for p in /lib64/ld-linux-x86-64.so.2 /lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 /lib/ld-linux-x86-64.so.2; do \
			[ -e "$$p" ] && { echo true; exit 0; }; \
		done; \
		if command -v ldd >/dev/null 2>&1; then \
			if ldd --version 2>&1 | tr '[:upper:]' '[:lower:]' | awk '/gnu libc|glibc/{found=1} END{exit !found}'; then \
				echo true; \
			else \
				echo false; \
			fi; \
		else \
			echo false; \
		fi; \
	else \
		echo false; \
	fi)

# Enable glibc build tag for NVML on supported Linux builds.
AGENT_GO_TAGS :=
ifeq ($(NVML),true)
AGENT_GO_TAGS := -tags glibc
else ifeq ($(NVML),auto)
ifeq ($(HOST_GLIBC),true)
AGENT_GO_TAGS := -tags glibc
endif
endif

.PHONY: build clean test dev golint docker-build docker-smart-devices docker-compose-override docker-up docker-down
.DEFAULT_GOAL := build

IMAGE ?= go-monitoring:local
CONTAINER ?= go-monitoring
DOCKER_DIR := docker
DOCKER_COMPOSE_FILE := $(DOCKER_DIR)/docker-compose.yml
DOCKER_COMPOSE_OVERRIDE := $(DOCKER_DIR)/docker-compose.override.yml

golint:
	@echo "🔎 Linting Go module in: $(BACKEND_DIR)"
	@echo "   Running gofmt..."
ifneq ($(CI),)
	@fmt_out="$$(cd "$(BACKEND_DIR)" && "$(GOFMT)" -s -l .)"; \
	if [ -n "$$fmt_out" ]; then echo "The following files are not gofmt'ed:"; echo "$$fmt_out"; exit 1; fi
else
	@( cd "$(BACKEND_DIR)" && "$(GOFMT)" -s -w . )
endif
	@echo "   Ensuring go.mod is tidy..."
	@( cd "$(BACKEND_DIR)" && $(GO_CMD_ENV) "$(GO_BIN)" mod tidy && $(GO_CMD_ENV) "$(GO_BIN)" mod download )
	@echo "   Running modernize..."
	@( cd "$(BACKEND_DIR)" && $(GO_CMD_ENV) GOFLAGS="-buildvcs=false" "$(GO_BIN)" run "$(MODERNIZE_MODULE)@$(MODERNIZE_VERSION)" -fix ./... )
	@echo "   Running golangci-lint..."
	@( cd "$(BACKEND_DIR)" && $(GO_CMD_ENV) "$(GOLANGCI_LINT)" run --fix ./... --timeout 3m $(GOLANGCI_LINT_OPTS) )
	@echo "✅ Go linting passed!"


clean:
	@( cd "$(BACKEND_DIR)" && $(GO_CMD_ENV) "$(GO_BIN)" clean )
	rm -f "$(BUILD_OUTPUT)"

test:
	@( cd "$(BACKEND_DIR)" && $(GO_CMD_ENV) "$(GO_BIN)" test ./... )

build:
	@( cd "$(BACKEND_DIR)" && GOOS=$(OS) GOARCH=$(ARCH) $(GOAMD64_ENV) $(GO_CMD_ENV) "$(GO_BIN)" build $(AGENT_GO_TAGS) -o "$(BUILD_OUTPUT)" -ldflags "$(LDFLAGS)" $(AGENT_PKG) )

docker-build:
	docker build --build-arg GOAMD64="$(GOAMD64)" --build-arg NVML="$(NVML)" -f "$(DOCKER_DIR)/Dockerfile" -t "$(IMAGE)" .

docker-smart-devices:
	@./$(DOCKER_DIR)/discover-smart-devices.sh summary

docker-compose-override:
	@./$(DOCKER_DIR)/discover-smart-devices.sh compose > "$(DOCKER_COMPOSE_OVERRIDE)"
	@echo "Generated $(DOCKER_COMPOSE_OVERRIDE)"

docker-up: docker-compose-override
	docker compose -f "$(DOCKER_COMPOSE_FILE)" -f "$(DOCKER_COMPOSE_OVERRIDE)" down --remove-orphans
	docker compose -f "$(DOCKER_COMPOSE_FILE)" -f "$(DOCKER_COMPOSE_OVERRIDE)" up --build

docker-down:
	docker compose -f "$(DOCKER_COMPOSE_FILE)" down

dev:
	@if command -v entr >/dev/null 2>&1; then \
		find "$(BACKEND_DIR)/internal" "$(BACKEND_DIR)/pkg" -type f -name '*.go' | entr -r sh -c 'cd "$(BACKEND_DIR)" && $(GO_CMD_ENV) "$(GO_BIN)" run $(AGENT_GO_TAGS) $(AGENT_PKG)'; \
	else \
		cd "$(BACKEND_DIR)" && $(GO_CMD_ENV) "$(GO_BIN)" run $(AGENT_GO_TAGS) $(AGENT_PKG); \
	fi
