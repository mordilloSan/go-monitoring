# Recipes use bash features (set -o pipefail); the default /bin/sh is dash on
# Debian/Ubuntu and older dash rejects pipefail.
SHELL := /usr/bin/env bash

REPO_ROOT := $(patsubst %/,%,$(dir $(abspath $(firstword $(MAKEFILE_LIST)))))

# Go project root autodetection
BACKEND_DIR := $(shell \
  if [ -f "$(REPO_ROOT)/backend/go.mod" ]; then echo "$(REPO_ROOT)/backend"; \
  elif [ -f "$(REPO_ROOT)/go.mod" ]; then echo "$(REPO_ROOT)"; \
  else echo ""; fi )
ifeq ($(BACKEND_DIR),)
$(error Could not find go.mod in backend/ or project root)
endif

GO_VERSION ?= $(shell awk '/^go / {print $$2; exit}' "$(BACKEND_DIR)/go.mod")
GO_TOOLS_DIR ?= $(HOME)/.go
GO_INSTALL_DIR ?= $(GO_TOOLS_DIR)
GOLANGCI_LINT_OPTS ?= --modules-download-mode=mod
AGENT_PKG    := .
BUILD_OUTPUT  = $(REPO_ROOT)/go-monitoring
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
GO_TOOLCHAIN ?= auto
SKIP_ENSURE_GO ?= 0
GO_CMD_ENV = env PATH="$(GO_INSTALL_DIR)/bin:$(GO_TOOLS_DIR)/bin:$$PATH" GOTOOLCHAIN=$(GO_TOOLCHAIN)
# Tool versions are pinned so CI runs are reproducible and cacheable.
GOLANGCI_LINT_MODULE  := github.com/golangci/golangci-lint/v2/cmd/golangci-lint
GOLANGCI_LINT_VERSION ?= v2.12.2
GOLANGCI_LINT         := $(GO_TOOLS_DIR)/bin/golangci-lint
MODERNIZE_MODULE      := golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize
MODERNIZE_VERSION     ?= v0.47.0
DEADCODE_MODULE       := golang.org/x/tools/cmd/deadcode
DEADCODE_VERSION      ?= v0.47.0
DEADCODE              := $(GO_TOOLS_DIR)/bin/deadcode
GO_BUILD_PREREQ := ensure-go
ifeq ($(SKIP_ENSURE_GO),1)
GO_BUILD_PREREQ :=
endif

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
NVML ?= true

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

.PHONY: build clean test check-backend test-backend dev install uninstall golint golint-only deadcode deadcode-only ensure-go ensure-golint ensure-deadcode
.DEFAULT_GOAL := build

ensure-go:
	@set -euo pipefail; \
	if [ -z "$(GO_VERSION)" ]; then \
		echo "ERROR: GO_VERSION is empty; check $(BACKEND_DIR)/go.mod"; \
		exit 1; \
	fi; \
	out="$$( $(GO_CMD_ENV) "$(GO_BIN)" version 2>/dev/null || true )"; \
	if ! printf '%s\n' "$$out" | grep -Fq "go$(GO_VERSION) "; then \
		echo "ERROR: expected Go $(GO_VERSION), got: $${out:-not found}"; \
		exit 1; \
	fi; \
	echo "✅ Go $(GO_VERSION) ready."

ensure-golint: ensure-go
	@set -euo pipefail; \
	bin="$(GOLANGCI_LINT)"; need=1; \
	if [ -x "$$bin" ]; then \
		out="$$( "$$bin" version 2>/dev/null || true )"; \
		ver="$$( printf '%s' "$$out" | sed -n 's/^golangci-lint has version[[:space:]]\([v0-9.]\+\).*/\1/p' )"; \
		ver_no_v="$${ver#v}"; major="$${ver_no_v%%.*}"; \
		built_ok="$$( printf '%s' "$$out" | grep -Eq 'built with go$(subst .,\.,$(GO_VERSION))([[:space:]]|$$)' && echo yes || echo no )"; \
		if [ "$(GOLANGCI_LINT_VERSION)" != "latest" ] && [ "v$$ver_no_v" != "$(GOLANGCI_LINT_VERSION)" ]; then \
			built_ok=no; \
		fi; \
		if [ "$$major" = "2" ] && [ "$$built_ok" = "yes" ]; then need=0; fi; \
	fi; \
	if [ "$$need" -eq 1 ]; then \
		echo "📥 Installing golangci-lint $(GOLANGCI_LINT_VERSION) (v2) with local Go ($(GO_BIN))..."; \
		rm -f "$$bin" || true; \
		$(GO_CMD_ENV) GOBIN="$(GO_TOOLS_DIR)/bin" GOFLAGS="-buildvcs=false" \
			"$(GO_BIN)" install "$(GOLANGCI_LINT_MODULE)@$(GOLANGCI_LINT_VERSION)"; \
	fi; \
	"$$bin" version | head -n1; \
	out="$$( "$$bin" version )"; \
	ver="$$( printf '%s' "$$out" | sed -n 's/^golangci-lint has version[[:space:]]\([v0-9.]\+\).*/\1/p' )"; \
	ver_no_v="$${ver#v}"; major="$${ver_no_v%%.*}"; \
	[ "$$major" = "2" ] || { echo "ERROR: golangci-lint is not v2"; exit 1; }; \
	printf '%s\n' "$$out" | grep -Eq 'built with go$(subst .,\.,$(GO_VERSION))([[:space:]]|$$)' || { echo "ERROR: golangci-lint not built with Go $(GO_VERSION)"; exit 1; }; \
	echo "✅ golangci-lint v2 ready."

ensure-deadcode: ensure-go
	@set -euo pipefail; \
	bin="$(DEADCODE)"; \
	if [ ! -x "$$bin" ]; then \
		echo "📥 Installing deadcode $(DEADCODE_VERSION) with local Go ($(GO_BIN))..."; \
		$(GO_CMD_ENV) GOBIN="$(GO_TOOLS_DIR)/bin" GOFLAGS="-buildvcs=false" \
			"$(GO_BIN)" install "$(DEADCODE_MODULE)@$(DEADCODE_VERSION)"; \
	fi; \
	"$$bin" -h >/dev/null 2>&1 || { echo "ERROR: deadcode is installed but not runnable"; exit 1; }; \
	echo "✅ deadcode ready."

golint: ensure-golint
	@$(MAKE) --no-print-directory golint-only SKIP_ENSURE_GO=1

golint-only: $(GO_BUILD_PREREQ)
	@echo "🔎 Linting Go module in: $(BACKEND_DIR)"
	@echo "   Running Go formatters..."
ifneq ($(CI),)
	@fmt_out="$$(cd "$(BACKEND_DIR)" && $(GO_CMD_ENV) "$(GOLANGCI_LINT)" fmt --diff 2>&1)"; \
	status=$$?; \
	if [ $$status -ne 0 ]; then echo "$$fmt_out"; exit $$status; fi; \
	if [ -n "$$fmt_out" ]; then echo "Go files need formatting:"; echo "$$fmt_out"; exit 1; fi
else
	@( cd "$(BACKEND_DIR)" && $(GO_CMD_ENV) "$(GOLANGCI_LINT)" fmt )
endif
	@echo "   Ensuring go.mod is tidy..."
	@( cd "$(BACKEND_DIR)" && $(GO_CMD_ENV) "$(GO_BIN)" mod tidy && $(GO_CMD_ENV) "$(GO_BIN)" mod download )
	@echo "   Running modernize..."
	@( cd "$(BACKEND_DIR)" && $(GO_CMD_ENV) GOFLAGS="-buildvcs=false" "$(GO_BIN)" run "$(MODERNIZE_MODULE)@$(MODERNIZE_VERSION)" -fix ./... )
	@echo "   Running golangci-lint..."
	@( cd "$(BACKEND_DIR)" && $(GO_CMD_ENV) "$(GOLANGCI_LINT)" run ./... --timeout 3m $(GOLANGCI_LINT_OPTS) )
	@echo "✅ Go linting passed!"

check-backend: ensure-go ensure-golint ensure-deadcode
	@set -uo pipefail; \
	ST=0; \
	$(MAKE) --no-print-directory golint-only SKIP_ENSURE_GO=1 || ST=1; \
	echo ""; \
	$(MAKE) --no-print-directory test-backend SKIP_ENSURE_GO=1 || ST=1; \
	echo ""; \
	$(MAKE) --no-print-directory deadcode-only SKIP_ENSURE_GO=1 || true; \
	if [ $$ST -ne 0 ]; then \
		echo "❌ Backend checks failed."; \
		exit 1; \
	fi; \
	echo "✅ Backend checks passed!"

test-backend: $(GO_BUILD_PREREQ)
	@echo "🧪 Running Go unit tests..."
	@cd "$(BACKEND_DIR)" && \
		out="$$( $(GO_CMD_ENV) GOFLAGS="-buildvcs=false" "$(GO_BIN)" test ./... -tags testing -count=1 -timeout 5m 2>&1 )"; \
		status=$$?; \
		printf '%s\n' "$$out" | grep -v '\[no test files\]' || true; \
		exit $$status

deadcode: ensure-deadcode
	@$(MAKE) --no-print-directory deadcode-only SKIP_ENSURE_GO=1

deadcode-only: $(GO_BUILD_PREREQ)
	@echo "🔎 Scanning Go module for dead code (informational)..."
	@cd "$(BACKEND_DIR)" && \
		out="$$( $(GO_CMD_ENV) "$(DEADCODE)" -test ./... 2>&1 )"; \
		status=$$?; \
		if [ $$status -ne 0 ]; then \
			echo "⚠️  deadcode scan could not complete (informational, not failing):"; \
			printf '%s\n' "$$out"; \
		elif [ -n "$$out" ]; then \
			echo "⚠️  deadcode found unreachable functions (informational, not failing):"; \
			printf '%s\n' "$$out"; \
		else \
			echo "✅ No dead code found!"; \
		fi


clean:
	@( cd "$(BACKEND_DIR)" && $(GO_CMD_ENV) "$(GO_BIN)" clean )
	rm -f "$(BUILD_OUTPUT)"

test: check-backend

build:
	@( cd "$(BACKEND_DIR)" && GOOS=$(OS) GOARCH=$(ARCH) $(GOAMD64_ENV) $(GO_CMD_ENV) "$(GO_BIN)" build $(AGENT_GO_TAGS) -o "$(BUILD_OUTPUT)" -ldflags "$(LDFLAGS)" $(AGENT_PKG) )

# Standard GNU-style install: `sudo make install` puts the binary on PATH at
# /usr/local/bin; override with PREFIX/DESTDIR for packaging or custom layouts.
PREFIX ?= /usr/local
BINDIR  = $(DESTDIR)$(PREFIX)/bin

install: build
	install -d "$(BINDIR)"
	install -m 0755 "$(BUILD_OUTPUT)" "$(BINDIR)/go-monitoring"

uninstall:
	rm -f "$(BINDIR)/go-monitoring"

dev:
	@if command -v entr >/dev/null 2>&1; then \
		find "$(BACKEND_DIR)/internal" "$(BACKEND_DIR)/pkg" -type f -name '*.go' | entr -r sh -c 'cd "$(BACKEND_DIR)" && LOG_LEVEL=$${LOG_LEVEL:-debug} $(GO_CMD_ENV) "$(GO_BIN)" run $(AGENT_GO_TAGS) $(AGENT_PKG)'; \
	else \
		cd "$(BACKEND_DIR)" && LOG_LEVEL=$${LOG_LEVEL:-debug} $(GO_CMD_ENV) "$(GO_BIN)" run $(AGENT_GO_TAGS) $(AGENT_PKG); \
	fi
