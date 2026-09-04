.PHONY: frontend build dev-frontend dev-backend dev-proxy dev-transcode lint test test-go test-web embed-stub clean jellyfin-web migrate-continuum-check verify-local-paths install-hooks migrate-create migrate-validate migrate-status migrate-up migrate-down-to settings-bindings verify-settings-bindings verify-settings-bindings-web verify-settings-bindings-all playback-fixtures verify-playback-fixtures route-inventory verify-route-inventory lint-router-recovery verify-migration-ledger verify-scenario-catalogs offline-routes verify-offline-routes apiv2-openapi verify-apiv2-openapi verify-apiv2-contract

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
BUILD_NUMBER ?=
BUILD_DATE ?=
GO_LDFLAGS := -X $(BUILDINFO_PKG).revisionOverride=$(BUILD_REVISION) -X $(BUILDINFO_PKG).dirtyOverride=$(BUILD_DIRTY) -X $(BUILDINFO_PKG).buildNumberOverride=$(BUILD_NUMBER) -X $(BUILDINFO_PKG).builtAtOverride=$(BUILD_DATE)

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

# Frontend test files that fail on main today. This list is shrink-only: delete
# an entry along with its fix, and never extend it to land a change. The Go
# suite has no equivalent — a Go test that cannot pass yet carries a t.Skip and
# its reason in the source, where whoever reads the test finds it.
WEBTEST_KNOWN_FAILURES := \
	--exclude src/pages/Catalog.test.tsx \
	--exclude src/pages/ItemDetail/SeasonContent.test.tsx \
	--exclude src/pages/LibraryRecommended.test.tsx \
	--exclude src/pages/setup-wizard/steps/ServerStorageStep.test.tsx \
	--exclude src/player/hooks/useASSSubtitles.test.tsx

# The Go binary embeds the built frontend, so every Go build and test needs
# web/dist to exist. Tests never serve it, so a placeholder is enough; `make
# build` still builds the real bundle.
embed-stub:
	@mkdir -p web/dist
	@[ -e web/dist/index.html ] || printf '<!doctype html>\n' > web/dist/index.html

# Run the Go and frontend test suites.
test: test-go test-web

test-go: embed-stub
	go test ./...

test-web:
	cd web && pnpm exec vitest run $(WEBTEST_KNOWN_FAILURES)

# Regenerate the settings-contract bindings for every language.
#
# The client repos are siblings of this one (see CLAUDE.md); a missing checkout
# is skipped rather than failing, so a server-only developer can still run this.
#
# The conformance fixture (contracts/settings/v1/conformance.json) travels with
# the bindings: the vendored copy in web/src/lib is what the web runner reads.
# The Kotlin and Swift copies land together with their runners in the client
# repos, which will pick their own test-resource paths.
SILO_ANDROID_DIR ?= $(abspath ../silo-android)
SILO_APPLE_DIR ?= $(abspath ../silo-apple)

settings-bindings:
	@mkdir -p internal/settingskeys
	go run ./cmd/settingsgen -lang go -out internal/settingskeys/keys.go
	gofmt -w internal/settingskeys/keys.go
	go run ./cmd/settingsgen -lang ts -out web/src/lib/settingsContract.ts
	@cd web && pnpm exec prettier --write src/lib/settingsContract.ts >/dev/null
	cp contracts/settings/v1/conformance.json web/src/lib/settingsConformance.json
	@if [ -d "$(SILO_ANDROID_DIR)" ]; then \
		go run ./cmd/settingsgen -lang kotlin \
			-out "$(SILO_ANDROID_DIR)/shared/src/commonMain/kotlin/org/siloserver/silo/model/settings/SettingKeys.kt"; \
		echo "wrote Kotlin bindings to $(SILO_ANDROID_DIR)"; \
	else \
		echo "skipping Kotlin: $(SILO_ANDROID_DIR) not checked out"; \
	fi
	@if [ -d "$(SILO_APPLE_DIR)" ]; then \
		go run ./cmd/settingsgen -lang swift \
			-out "$(SILO_APPLE_DIR)/iosApp/iosApp/Networking/SettingKeys.generated.swift"; \
		echo "wrote Swift bindings to $(SILO_APPLE_DIR)"; \
	else \
		echo "skipping Swift: $(SILO_APPLE_DIR) not checked out"; \
	fi

# Fail when the committed bindings disagree with the manifest, so a manifest
# change cannot merge without regenerating what every client reads.
#
# Split in two because the generated TypeScript is compared after prettier, and
# only the Web CI job has pnpm: the Go job runs this target, the Web job runs
# verify-settings-bindings-web. Locally, `verify-settings-bindings-all` is both.
verify-settings-bindings:
	@CHECK_DIR=$$(mktemp -d) && trap 'rm -rf "$$CHECK_DIR"' EXIT && \
	go run ./cmd/settingsgen -lang go | gofmt > "$$CHECK_DIR/keys.go" && \
	diff -u internal/settingskeys/keys.go "$$CHECK_DIR/keys.go" \
		|| { echo "::error::internal/settingskeys/keys.go is stale; run make settings-bindings"; exit 1; }
	@diff -u web/src/lib/settingsConformance.json contracts/settings/v1/conformance.json \
		|| { echo "::error::web/src/lib/settingsConformance.json is stale; run make settings-bindings"; exit 1; }
	@echo "settings bindings are current"

# The half that needs pnpm: regenerate the web binding, format it the way the
# bindings target does, and compare. Without this a manifest change could merge
# with a stale settingsContract.ts, which is what every web control renders from.
verify-settings-bindings-web:
	@CHECK_DIR=$$(mktemp -d) && trap 'rm -rf "$$CHECK_DIR"' EXIT && \
	go run ./cmd/settingsgen -lang ts -out "$$CHECK_DIR/settingsContract.ts" && \
	cd web && pnpm exec prettier --log-level silent --config .prettierrc \
		--write "$$CHECK_DIR/settingsContract.ts" && cd .. && \
	diff -u web/src/lib/settingsContract.ts "$$CHECK_DIR/settingsContract.ts" \
		|| { echo "::error::web/src/lib/settingsContract.ts is stale; run make settings-bindings"; exit 1; }
	@echo "web settings binding is current"

verify-settings-bindings-all: verify-settings-bindings verify-settings-bindings-web

# Regenerate the protocol-v3 golden contract fixtures from the live types and planner.
#
# The server owns the playback contract and the clients prove conformance
# against these bodies, so they are only trustworthy while the code that emits
# them is the code that serves traffic. Editing one by hand instead of running
# this would let the contract and the implementation drift apart in silence.
PLAYBACK_FIXTURE_DIR := internal/playback/testdata/protocol_v3
PLAYBACK_SCHEMA_FIXTURE_DIR := docs/design/schemas/playback-v3/v3/fixtures/valid
PLAYBACK_WIRE_FIXTURES := start_request.json replan_request.json decision_response.json capability_response.json error_response.json route_event.json

playback-fixtures:
	go run ./cmd/playbackfixtures -out $(PLAYBACK_FIXTURE_DIR)
	@set -e; for fixture in $(PLAYBACK_WIRE_FIXTURES); do \
		cp "$(PLAYBACK_FIXTURE_DIR)/$$fixture" "$(PLAYBACK_SCHEMA_FIXTURE_DIR)/$$fixture"; \
	done

# Fail when the committed fixtures disagree with the contract types. A change
# that does not regenerate leaves every client testing against a body the server
# no longer produces, which is exactly the drift the fixtures exist to catch.
verify-playback-fixtures:
	@CHECK_DIR=$$(mktemp -d) && trap 'rm -rf "$$CHECK_DIR"' EXIT && \
	go run ./cmd/playbackfixtures -out "$$CHECK_DIR" && \
	diff -ur $(PLAYBACK_FIXTURE_DIR) "$$CHECK_DIR" \
		|| { echo "::error::$(PLAYBACK_FIXTURE_DIR) is stale; run make playback-fixtures"; exit 1; }; \
	for fixture in $(PLAYBACK_WIRE_FIXTURES); do \
		cmp -s "$$CHECK_DIR/$$fixture" "$(PLAYBACK_SCHEMA_FIXTURE_DIR)/$$fixture" \
			|| { echo "::error::$(PLAYBACK_SCHEMA_FIXTURE_DIR)/$$fixture is stale; run make playback-fixtures"; exit 1; }; \
	done
	@echo "playback fixtures are current"

ROUTE_INVENTORY := contracts/api/v2/route-inventory.json

# Rebuild the legacy native route inventory from registration source.
route-inventory:
	go run ./cmd/route-inventory -out $(ROUTE_INVENTORY)

# Fail when the committed inventory disagrees with the registration source, or
# when the source contains a registration the generator cannot account for. The
# generator refuses to emit a partial inventory, so both failures land here
# rather than as a quietly shorter artifact.
verify-route-inventory:
	@go run ./cmd/route-inventory -check $(ROUTE_INVENTORY) \
		|| { echo "::error::$(ROUTE_INVENTORY) is stale or a route is unaccounted for; run make route-inventory"; exit 1; }

# Run the route inventory's router-recovery lint rule over the whole tree, not
# only changed lines. It is the one gocritic check the repo enables (see
# .golangci.yml), and the tree passes it today, so this can gate CI while the
# rest of `make lint` cannot.
lint-router-recovery:
	golangci-lint run --enable-only gocritic --max-same-issues=0 --max-issues-per-linter=0 ./...
MIGRATION_LEDGER := contracts/api/v2/migration.json

# Fail when the v2 migration ledger violates its JSON Schema, no longer covers
# the route inventory one-to-one, or breaks a review rule (removed rows are
# tier 2, ratified rows name an owner, only plugin-proxy handlers claim the
# dynamic_plugin_proxy override). Runs the whole internal/contractledger
# package so this named step enforces everything the docs attribute to it.
verify-migration-ledger:
	@go test -count=1 ./internal/contractledger/ \
		|| { echo "::error::$(MIGRATION_LEDGER) violates contracts/api/v2/migration.schema.json or disagrees with $(ROUTE_INVENTORY); see docs/architecture/api-contract.md (Migration ledger)"; exit 1; }

SCENARIO_CATALOG_DIR := contracts/api/v2/scenarios

# Fail when a tier-1 scenario catalog violates its JSON Schema, names a row
# the migration ledger does not carry at tier 1, files a row outside its route
# group's catalog, or leaves a tier-1 row of a declared wave without a scenario
# per applicable category. The executor that runs the scenarios against the
# router is a separate go test (./internal/scenariocatalog/executor); only its
# public subset runs in CI. The rest runs when SILO_SCENARIO_DATABASE_URL names
# an empty database the executor owns (not SILO_TEST_DATABASE_URL: it
# truncates).
verify-scenario-catalogs:
	@go test -count=1 -run '^TestCatalogsPassGate$$' ./internal/scenariocatalog/ \
		|| { echo "::error::$(SCENARIO_CATALOG_DIR) violates scenario-catalog.schema.json or leaves a tier-1 row uncovered"; exit 1; }

APIV2_OPENAPI := contracts/api/v2/openapi.json

# Regenerate the native API v2 OpenAPI artifact from the Go registries. The
# generator opens no database or network and reads no environment, so the
# output is byte-identical on every machine.
apiv2-openapi:
	go run ./cmd/apiv2-openapi -out $(APIV2_OPENAPI)

# Fail when the committed artifact differs from a fresh generation. The
# generator writes to a temporary directory and the two files are
# byte-compared, so a stale artifact, a reordered key or a stray timestamp all
# land here.
verify-apiv2-openapi:
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && \
		go run ./cmd/apiv2-openapi -out "$$tmp/openapi.json" && \
		cmp -s "$$tmp/openapi.json" $(APIV2_OPENAPI) \
		|| { echo "::error::$(APIV2_OPENAPI) is stale; run make apiv2-openapi"; exit 1; }
	@echo "$(APIV2_OPENAPI) is current"

OFFLINE_ROUTES := contracts/api/v2/offline-routes.txt

# The scenario executor decides run-vs-skip for a public scenario by whether
# the offline (no-database) API router registers its row. api.NewRouter returns
# a sealed handler nothing outside internal/api's tests can walk, so the answer
# is pinned in $(OFFLINE_ROUTES) by an in-package test that builds the same
# wiring through the unexported constructor. offline-routes regenerates the
# file; verify-offline-routes fails when it is stale.
offline-routes:
	go test -count=1 -run '^TestOfflineRouteSet$$' ./internal/api/ -update-offline-routes

verify-offline-routes:
	@go test -count=1 -run '^TestOfflineRouteSet$$' ./internal/api/ \
		|| { echo "::error::$(OFFLINE_ROUTES) disagrees with the offline API router; run make offline-routes"; exit 1; }

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

# Roll back every migration newer than VERSION (the version to KEEP).
#
# Not a routine operation: it discards data. It exists because some migrations
# are Go rather than SQL — the settings backfill and the jellycompat
# DisplayPreferences move — and those are registered in-process, so the goose
# CLI above cannot see or reverse them.
#
# This is a RANGE, not a list: everything newer than VERSION comes off, including
# migrations belonging to other features that happen to sort in between. Check
# `make migrate-status` and read the down of each one you are about to revert.
# Take a backup first regardless; the per-user SQLite stores have no down path.
#
# Usage: make migrate-down-to VERSION=<timestamp from migrate-status>
migrate-down-to:
	@if [ -z "$(VERSION)" ]; then echo "usage: make migrate-down-to VERSION=<timestamp from make migrate-status>"; exit 1; fi
	go run ./cmd/silo/ --env "$(ENV_FILE)" --migrate-down-to "$(VERSION)"

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
