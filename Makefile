.PHONY: frontend build dev-frontend dev-backend dev-proxy dev-transcode lint clean jellyfin-web migrate-continuum-check verify-local-paths install-hooks migrate-create migrate-validate migrate-status migrate-up

GIT_COMMON_DIR := $(strip $(shell git rev-parse --git-common-dir 2>/dev/null))
MAIN_CHECKOUT_ROOT := $(if $(GIT_COMMON_DIR),$(abspath $(GIT_COMMON_DIR)/..))
SHARED_MAKEFILE_LOCAL := $(if $(GIT_COMMON_DIR),$(abspath $(GIT_COMMON_DIR)/../Makefile.local))
DEFAULT_PLUGIN_SDK_DIR := $(abspath ../silo-plugin-sdk)
SHARED_PLUGIN_SDK_DIR := $(if $(MAIN_CHECKOUT_ROOT),$(abspath $(MAIN_CHECKOUT_ROOT)/../silo-plugin-sdk))
GOOSE := go run github.com/pressly/goose/v3/cmd/goose@v3.27.1
GOOSE_DIR := migrations/sql
ENV_FILE ?= .env

ifneq ($(wildcard $(DEFAULT_PLUGIN_SDK_DIR)),)
DEV_PLUGIN_SDK_DIR ?= $(DEFAULT_PLUGIN_SDK_DIR)
else ifneq ($(wildcard $(SHARED_PLUGIN_SDK_DIR)),)
DEV_PLUGIN_SDK_DIR ?= $(SHARED_PLUGIN_SDK_DIR)
endif

JELLYFIN_WEB_INSTALL_DIR ?= .local/compat/jellyfin-web
JELLYFIN_WEB_VERSION ?= 10.11.6

# Build version stamping: inject the git revision so the admin Build panel shows a
# version even when Go's VCS metadata isn't embedded (mirrors the Dockerfile ldflags).
BUILDINFO_PKG := github.com/Silo-Server/silo-server/internal/buildinfo
BUILD_REVISION ?= $(shell git rev-parse HEAD 2>/dev/null)
BUILD_DIRTY ?= $(shell test -n "$$(git status --porcelain 2>/dev/null)" && echo true || echo false)
GO_LDFLAGS := -X $(BUILDINFO_PKG).revisionOverride=$(BUILD_REVISION) -X $(BUILDINFO_PKG).dirtyOverride=$(BUILD_DIRTY)

# Build the frontend (requires pnpm)
frontend:
	cd web && pnpm install --frozen-lockfile && pnpm run build

# Build the Go binary (depends on frontend)
build: frontend
	go build -ldflags "$(GO_LDFLAGS)" -o silo ./cmd/silo/

# Run frontend dev server (proxies API to localhost:8080)
dev-frontend:
	cd web && pnpm run dev

# Run the Go backend (integrated mode)
dev-backend:
	go run ./cmd/silo/

# Run a proxy node (stateless stream proxy, no DB required)
dev-proxy:
	go run ./cmd/silo/ --mode=proxy

# Run a transcode node (HLS transcode worker, no DB required)
dev-transcode:
	go run ./cmd/silo/ --mode=transcode

# Lint Go and frontend code
lint:
	golangci-lint run
	cd web && pnpm run lint

# Check committed content for local machine path leaks.
verify-local-paths:
	scripts/check-local-path-leaks.sh

# Create a timestamped Goose SQL migration. Usage: make migrate-create NAME=add_thing
migrate-create:
	@if [ -z "$(NAME)" ]; then echo "usage: make migrate-create NAME=add_thing"; exit 1; fi
	$(GOOSE) -dir $(GOOSE_DIR) create "$(NAME)" sql

# Validate Goose migration annotations and SQL parsing without touching a database.
migrate-validate:
	$(GOOSE) -dir $(GOOSE_DIR) validate

# Show Goose migration status through Silo's bootstrapping runner.
migrate-status:
	go run ./cmd/silo/ --env "$(ENV_FILE)" --migrate-status

# Apply pending Goose migrations through Silo's bootstrapping runner.
migrate-up:
	go run ./cmd/silo/ --env "$(ENV_FILE)" --migrate-only

# Install repo-local git hooks for this checkout/worktree.
install-hooks:
	@existing="$$(git config --local core.hooksPath 2>/dev/null || true)"; \
	if [ -n "$$existing" ] && [ "$$existing" != ".githooks" ]; then \
		echo "warning: overwriting existing local core.hooksPath ($$existing) with .githooks"; \
	fi
	git config core.hooksPath .githooks

# Fetch and build the pinned Jellyfin Web component into a gitignored local cache.
jellyfin-web:
	go run ./cmd/silo/ compat-web install --dir "$(JELLYFIN_WEB_INSTALL_DIR)" --version "$(JELLYFIN_WEB_VERSION)"

# Read-only preflight for Continuum Docker installs moving to Silo.
migrate-continuum-check:
	scripts/migrate-continuum-docker.sh check

# Clean build artifacts
clean:
	rm -rf web/dist web/node_modules silo

# Include developer-specific targets (gitignored, optional).
# In Git worktrees, fall back to the main checkout's Makefile.local so custom
# targets like dev-deploy work without per-worktree symlinks or copies.
ifneq ($(wildcard Makefile.local),)
include Makefile.local
else ifneq ($(wildcard $(SHARED_MAKEFILE_LOCAL)),)
include $(SHARED_MAKEFILE_LOCAL)
endif
