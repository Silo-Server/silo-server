.PHONY: frontend build dev-frontend dev-backend dev-proxy dev-transcode lint test test-go test-web embed-stub clean jellyfin-web migrate-continuum-check verify-local-paths install-hooks migrate-create migrate-validate migrate-status migrate-up migrate-down-to settings-bindings verify-settings-bindings verify-settings-bindings-web verify-settings-bindings-all playback-fixtures verify-playback-fixtures route-inventory verify-route-inventory lint-router-recovery verify-migration-ledger verify-scenario-catalogs offline-routes verify-offline-routes apiv2-openapi verify-apiv2-openapi verify-apiv2-contract apiv2-fixtures verify-apiv2-fixtures apiv2-fixtures-sync verify-apiv2-fixtures-siblings apiv2-web-types verify-apiv2-web-types

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
	--exclude src/pages/setup-wizard/steps/ServerStorageStep.test.tsx

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
PLAYBACK_WIRE_FIXTURES := start_request.json replan_request.json decision_response.json native-decision_response.json capability_response.json error_response.json route_event.json

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
	@python3 scripts/apiv2-ledger/test_extract_consumers.py
	@python3 scripts/apiv2-ledger/assign_sections.py --check $(MIGRATION_LEDGER) \
		|| { echo "::error::$(MIGRATION_LEDGER) section assignments are stale; run scripts/apiv2-ledger/assign_sections.py"; exit 1; }
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

# Regenerate the web contract types from the committed OpenAPI artifact.
# openapi-typescript is pinned exactly in web/package.json, and the output is
# run through the repository prettier config, so the artifact is byte-stable.
APIV2_WEB_TYPES_DIR := web/src/api/v2
apiv2-web-types:
	cd web && pnpm run --silent generate:apiv2

# Fail when the committed web types disagree with a fresh generation from the
# committed OpenAPI artifact: a spec change that does not regenerate would
# leave the web client typed against a contract the server no longer serves.
verify-apiv2-web-types:
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && \
		( cd web && node scripts/generate-apiv2.mjs --out-dir "$$tmp" ) && \
		for f in schema.ts operations.ts; do \
			cmp -s "$$tmp/$$f" $(APIV2_WEB_TYPES_DIR)/$$f \
			|| { echo "::error::$(APIV2_WEB_TYPES_DIR)/$$f is stale; run make apiv2-web-types"; exit 1; }; \
		done
	@echo "$(APIV2_WEB_TYPES_DIR) is current"

# Semantic diff and spec lint over the committed artifact. BASE_REF names the
# merge base the diff compares against (CI passes the PR's base). The diff
# tool is the oasdiff Go module pinned in go.mod/go.sum; no binary is
# downloaded. A breaking change fails unless contracts/api/v2/
# breaking-approvals.json approves that exact operation and fingerprint;
# once contracts/api/v2/LOCKED exists no approval applies. The spec lint is
# the internal/contractspec tests, which also run the seeded breaking fixture
# through the tool so an upgrade that stops detecting it fails here.
BASE_REF ?= origin/main
verify-apiv2-contract:
	@go test -count=1 ./internal/contractspec/ \
		|| { echo "::error::$(APIV2_OPENAPI) fails the spec lint or the diff tool no longer detects the seeded breaking fixture"; exit 1; }
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && \
		base=$$(git merge-base $(BASE_REF) HEAD) && \
		{ git show "$$base:$(APIV2_OPENAPI)" > "$$tmp/base.json" 2>/dev/null || : ; } && \
		go run ./cmd/apiv2-contract-diff -base "$$tmp/base.json" -revision $(APIV2_OPENAPI) -contracts contracts/api/v2 \
		|| { echo "::error::$(APIV2_OPENAPI) has an unapproved breaking change against $$base; see contracts/api/v2/breaking-approvals.schema.json"; exit 1; }

# Regenerate the native API v2 contract fixtures through the real v2 router.
# The bodies are what the server answers, byte for byte, for synthetic
# requests with fixed request ids; nothing is hand-edited. verify-apiv2-fixtures
# fails when the committed tree is stale, then validates every body against
# the OpenAPI schema its index entry names (internal/contractspec).
APIV2_FIXTURE_DIR := contracts/api/v2/fixtures
apiv2-fixtures:
	go test -count=1 -run '^TestContractFixtures$$' ./internal/apiv2/ -update-apiv2-fixtures

verify-apiv2-fixtures:
	@go test -count=1 -run '^TestContractFixtures$$' ./internal/apiv2/ \
		|| { echo "::error::$(APIV2_FIXTURE_DIR) disagrees with the v2 router; run make apiv2-fixtures"; exit 1; }
	@go test -count=1 -run '^TestCommittedFixturesValidate$$' ./internal/contractspec/ \
		|| { echo "::error::$(APIV2_FIXTURE_DIR) fails OpenAPI schema validation"; exit 1; }
	@echo "$(APIV2_FIXTURE_DIR) is current and valid"

# Copy the v2 fixtures into the sibling client checkouts, the way the
# playback-v3 fixtures are vendored there: the tree plus a SOURCE file naming
# the server commit they came from. A missing checkout is skipped.
# verify-apiv2-fixtures-siblings reports which sibling copies are stale
# without writing anything.
APIV2_FIXTURES_APPLE := $(SILO_APPLE_DIR)/iosApp/Tests/Fixtures/APIv2
APIV2_FIXTURES_ANDROID := $(SILO_ANDROID_DIR)/shared/src/commonTest/resources/api/v2/fixtures
apiv2-fixtures-sync:
	@set -e; for dest in "$(APIV2_FIXTURES_APPLE)" "$(APIV2_FIXTURES_ANDROID)"; do \
		root=$${dest%%/iosApp/*}; root=$${root%%/shared/*}; \
		if [ ! -d "$$root" ]; then echo "skipping $$dest: $$root not checked out"; continue; fi; \
		mkdir -p "$$dest"; \
		find "$$dest" -maxdepth 1 -name '*.json' -delete; \
		cp $(APIV2_FIXTURE_DIR)/*.json "$$dest/"; \
		cp contracts/api/v2/fixtures.schema.json "$$dest/"; \
		printf 'Source: silo-server %s (plus %s)\nServer ref: %s\nServer commit: %s\nVendored: %s\n\nGenerated by make apiv2-fixtures; refresh with make apiv2-fixtures-sync from the server commit above.\n' \
			"$(APIV2_FIXTURE_DIR)" "contracts/api/v2/fixtures.schema.json" "$$(git rev-parse --abbrev-ref HEAD)" "$$(git rev-parse HEAD)" "$$(date -u +%Y-%m-%d)" > "$$dest/SOURCE"; \
		echo "wrote v2 fixtures to $$dest"; \
	done

verify-apiv2-fixtures-siblings:
	@status=0; for dest in "$(APIV2_FIXTURES_APPLE)" "$(APIV2_FIXTURES_ANDROID)"; do \
		root=$${dest%%/iosApp/*}; root=$${root%%/shared/*}; \
		if [ ! -d "$$root" ]; then echo "skipping $$dest: $$root not checked out"; continue; fi; \
		if [ ! -d "$$dest" ]; then echo "$$dest: not vendored yet; run make apiv2-fixtures-sync"; status=1; continue; fi; \
		cmp -s contracts/api/v2/fixtures.schema.json "$$dest/fixtures.schema.json" || { echo "$$dest/fixtures.schema.json is stale"; status=1; }; \
		names=""; \
		for f in "$$dest"/*.json; do \
			b=$$(basename "$$f"); \
			case "$$b" in fixtures.schema.json|index.json) continue;; esac; \
			names="$$names $${b%.json}"; \
			if [ -e "$(APIV2_FIXTURE_DIR)/$$b" ]; then cmp -s "$(APIV2_FIXTURE_DIR)/$$b" "$$f" || { echo "$$f is stale"; status=1; }; \
			else echo "$$f has no server counterpart"; status=1; fi; \
		done; \
		if command -v jq >/dev/null 2>&1; then \
			want=$$(jq -S --arg names "$$names" '.fixtures |= map(select(.name as $$n | ($$names | split(" ")) | index($$n)))' $(APIV2_FIXTURE_DIR)/index.json); \
			have=$$(jq -S . "$$dest/index.json" 2>/dev/null); \
			[ "$$want" = "$$have" ] || { echo "$$dest/index.json is stale (entries differ from the server index for the vendored fixtures)"; status=1; }; \
		else echo "jq not installed; skipped the $$dest/index.json entry check"; fi; \
	done; \
	[ "$$status" -eq 0 ] && echo "sibling v2 fixture copies are current" || exit 1

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

# Required paired profile-list acceptance owns a dedicated, empty scenario DB.
.PHONY: test-scenario-profile-pairing
test-scenario-profile-pairing:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredProfileListAcceptance$$' ./internal/scenariocatalog/executor

# Required paired device-list acceptance uses the same guarded scratch DB.
.PHONY: test-scenario-device-pairing
test-scenario-device-pairing:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredDeviceListAcceptance$$' ./internal/scenariocatalog/executor

# Required mutation effects and sibling preservation on the guarded scratch DB.
.PHONY: test-scenario-device-mutations
test-scenario-device-mutations:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredDeviceMutationAcceptance$$' ./internal/scenariocatalog/executor

# Required profile mutation effects on the guarded scratch database.
.PHONY: test-scenario-profile-mutations
test-scenario-profile-mutations:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredProfileMutationAcceptance$$' ./internal/scenariocatalog/executor

# Required paired reads of the acting profile's page customization.
.PHONY: test-scenario-section-reads
test-scenario-section-reads:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredSectionReadAcceptance$$' ./internal/scenariocatalog/executor

# Required paired resets preserve sibling profiles and other page scopes.
.PHONY: test-scenario-section-resets
test-scenario-section-resets:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredSectionResetAcceptance$$' ./internal/scenariocatalog/executor

# Required paired replacements verify stored effects and household isolation.
.PHONY: test-scenario-section-replacements
test-scenario-section-replacements:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredSectionReplaceAcceptance$$' ./internal/scenariocatalog/executor

# NEW admin source browsing through the real router and filesystem provider.
.PHONY: test-scenario-new-catalog-sources
test-scenario-new-catalog-sources:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredNewCatalogSourceBrowse$$' ./internal/scenariocatalog/executor

# NEW real-router catalog reads; these are outside the frozen scenario oracle.
.PHONY: test-scenario-new-catalog-reads
test-scenario-new-catalog-reads:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredNewCatalogMediaReads$$' ./internal/scenariocatalog/executor

# NEW real notification inbox and persisted read effects; outside frozen oracle.
.PHONY: test-scenario-new-notification-inbox
test-scenario-new-notification-inbox:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredNewNotificationInbox$$' ./internal/scenariocatalog/executor

# NEW real literary administration and persisted decisions; outside frozen oracle.
.PHONY: test-scenario-new-literary-admin
test-scenario-new-literary-admin:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredNewLiteraryAdmin$$' ./internal/scenariocatalog/executor

# NEW persisted diagnostic download failure paths; outside frozen oracle.
.PHONY: test-scenario-new-diagnostic-download
test-scenario-new-diagnostic-download:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredNewDiagnosticDownloadFailures$$' ./internal/scenariocatalog/executor

# NEW real person metadata updates; outside frozen oracle.
.PHONY: test-scenario-new-person-curation
test-scenario-new-person-curation:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredNewPersonCuration$$' ./internal/scenariocatalog/executor

# NEW real diagnostic history and metadata deletion; outside frozen oracle.
.PHONY: test-scenario-new-diagnostic-history
test-scenario-new-diagnostic-history:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredNewDiagnosticHistory$$' ./internal/scenariocatalog/executor

# NEW real catalog item metadata updates; outside frozen oracle.
.PHONY: test-scenario-new-item-curation
test-scenario-new-item-curation:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredNewItemCuration$$' ./internal/scenariocatalog/executor

# NEW persisted translation-job reads/cancellation; outside frozen oracle.
.PHONY: test-scenario-new-translation-jobs
test-scenario-new-translation-jobs:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredNewTranslationJobs$$' ./internal/scenariocatalog/executor

# NEW real season/episode metadata updates; outside frozen oracle.
.PHONY: test-scenario-new-child-curation
test-scenario-new-child-curation:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredNewChildCuration$$' ./internal/scenariocatalog/executor

# NEW current viewer-library discovery; outside frozen oracle.
.PHONY: test-scenario-new-viewer-libraries
test-scenario-new-viewer-libraries:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredNewViewerLibraries$$' ./internal/scenariocatalog/executor

# NEW notification webhook destination reads; outside frozen oracle.
.PHONY: test-scenario-new-webhook-destinations
test-scenario-new-webhook-destinations:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredNewWebhookDestinations$$' ./internal/scenariocatalog/executor

# NEW administrator notification channel reads; outside frozen oracle.
.PHONY: test-scenario-new-server-channels
test-scenario-new-server-channels:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredNewServerChannels$$' ./internal/scenariocatalog/executor

# NEW administrator device metadata reads; outside frozen oracle.
.PHONY: test-scenario-new-admin-device-reads
test-scenario-new-admin-device-reads:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredNewAdminDeviceReads$$' ./internal/scenariocatalog/executor

# Seven outstanding frozen API-key deletion scenarios, paired with real effects.
.PHONY: test-scenario-api-key-deletions
test-scenario-api-key-deletions:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAPIKeyDeleteAcceptance$$' ./internal/scenariocatalog/executor

# Six frozen API-key list scenarios with full-table preservation and v2 secret absence.
.PHONY: test-scenario-api-key-lists
test-scenario-api-key-lists:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAPIKeyListAcceptance$$' ./internal/scenariocatalog/executor

# Six outstanding frozen API-key scope discovery scenarios.
.PHONY: test-scenario-api-key-scopes
test-scenario-api-key-scopes:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAPIKeyScopesAcceptance$$' ./internal/scenariocatalog/executor

# Five outstanding frozen API-key creation refusals with unchanged rows.
.PHONY: test-scenario-api-key-create-refusals
test-scenario-api-key-create-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAPIKeyCreateRefusalAcceptance$$' ./internal/scenariocatalog/executor

# Four outstanding frozen API-key creations with exact persisted effects.
.PHONY: test-scenario-api-key-creations
test-scenario-api-key-creations:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAPIKeyCreateAcceptance$$' ./internal/scenariocatalog/executor

# Seven outstanding frozen account password capability scenarios.
.PHONY: test-scenario-account-capability
test-scenario-account-capability:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAccountCapabilityAcceptance$$' ./internal/scenariocatalog/executor

# Five outstanding frozen sign-in provider discovery scenarios.
.PHONY: test-scenario-auth-providers
test-scenario-auth-providers:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAuthProvidersAcceptance$$' ./internal/scenariocatalog/executor

# Two frozen account authentication refusals with complete table preservation.
.PHONY: test-scenario-account-me-refusals
test-scenario-account-me-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -v -run '^TestRequiredAccountMeRefusalAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-signup-status
test-scenario-signup-status:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredSignupStatusAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-setup-status
test-scenario-setup-status:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredSetupStatusAcceptance$$' ./internal/scenariocatalog/executor

# Four frozen administrator build metadata reads.
.PHONY: test-scenario-build-info
test-scenario-build-info:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredBuildInfoAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-login-sessions
test-scenario-login-sessions:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredLoginSessionsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-device-capability
test-scenario-device-capability:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredDeviceCapabilityAcceptance$$' ./internal/scenariocatalog/executor

# Seven frozen liveness/readiness originals against the retained unversioned probes (v1 only).
.PHONY: test-scenario-probes
test-scenario-probes:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredRetainedProbeAcceptance$$' ./internal/scenariocatalog/executor

# Three frozen public reads recorded under an unreachable database.
.PHONY: test-scenario-outage
test-scenario-outage:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredOutageAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-device-lookup
test-scenario-device-lookup:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredDeviceLookupAcceptance$$' ./internal/scenariocatalog/executor

# Three remaining frozen administrator build authority refusals.
.PHONY: test-scenario-build-authority
test-scenario-build-authority:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredBuildAuthorityAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-device-lookup-errors
test-scenario-device-lookup-errors:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredDeviceLookupErrorsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-setup-refusals
test-scenario-setup-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredSetupRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-session-delete-refusals
test-scenario-session-delete-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredSessionDeleteRefusalsAcceptance$$' ./internal/scenariocatalog/executor

# Three frozen administrator resource authorization refusals only.
.PHONY: test-scenario-resource-refusals
test-scenario-resource-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredResourceRefusalAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-device-start-refusals
test-scenario-device-start-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredDeviceStartRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-refresh-refusals
test-scenario-refresh-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredRefreshRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-admin-invitation-refusals
test-scenario-admin-invitation-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAdminInvitationRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-admin-invitation-revoke-refusals
test-scenario-admin-invitation-revoke-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAdminInvitationRevokeRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-password-refusals
test-scenario-password-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredPasswordRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-password-authority
test-scenario-password-authority:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredPasswordAuthorityAcceptance$$' ./internal/scenariocatalog/executor

# Three frozen hardware inventory authorization refusals; no probes.
.PHONY: test-scenario-hardware-refusals
test-scenario-hardware-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredHardwareRefusalAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-logout-refusals
test-scenario-logout-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredLogoutRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-refusals
test-scenario-invite-code-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-create-refusals
test-scenario-invite-code-create-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeCreateRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-device-deny-refusals
test-scenario-device-deny-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredDeviceDenyRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-device-approve-refusals
test-scenario-device-approve-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredDeviceApproveRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-handoff-refusals
test-scenario-handoff-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredHandoffRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-delete-refusals
test-scenario-invite-code-delete-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeDeleteRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-update-refusals
test-scenario-invite-code-update-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeUpdateRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-approval-state-refusals
test-scenario-approval-state-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredApprovalStateRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-topup-refusals
test-scenario-invite-code-topup-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeTopUpRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-impersonation-refusals
test-scenario-impersonation-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredImpersonationRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-create-input
test-scenario-invite-code-create-input:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeCreateInputAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-update-input
test-scenario-invite-code-update-input:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeUpdateInputAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-topup-input
test-scenario-invite-code-topup-input:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeTopUpInputAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-login-input
test-scenario-login-input:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredLoginInputAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-delete-input
test-scenario-invite-code-delete-input:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeDeleteInputAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-admin-invitation-resend-refusals
test-scenario-admin-invitation-resend-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAdminInvitationResendRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-login-credentials
test-scenario-login-credentials:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredLoginCredentialsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-list-error
test-scenario-invite-code-list-error:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeListErrorAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-list-reads
test-scenario-invite-code-list-reads:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeListReadsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-device-poll-refusals
test-scenario-device-poll-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredDevicePollRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-admin-invitation-create-refusals
test-scenario-admin-invitation-create-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAdminInvitationCreateRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-device-poll-states
test-scenario-device-poll-states:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredDevicePollStatesAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-missing
test-scenario-invite-code-missing:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeMissingAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-admin-invitation-input-refusals
test-scenario-admin-invitation-input-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAdminInvitationInputRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-signup-refusals
test-scenario-signup-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredSignupRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-admin-invitation-resend-targets
test-scenario-admin-invitation-resend-targets:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAdminInvitationResendTargetsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-duplicate
test-scenario-invite-code-duplicate:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeDuplicateAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-signup-codes
test-scenario-signup-codes:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredSignupCodesAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-admin-invitation-role-refusals
test-scenario-admin-invitation-role-refusals:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAdminInvitationRoleRefusalsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-empty-update
test-scenario-invite-code-empty-update:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeEmptyUpdateAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-field-updates
test-scenario-invite-code-field-updates:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeFieldUpdatesAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-me-impersonation
test-scenario-me-impersonation:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredMeImpersonationAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-topups
test-scenario-invite-code-topups:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeTopUpsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-admin-invitation-revoke-targets
test-scenario-admin-invitation-revoke-targets:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAdminInvitationRevokeTargetsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-account-reads
test-scenario-account-reads:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAccountReadsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-logout-success
test-scenario-logout-success:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredLogoutSuccessAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-deletions
test-scenario-invite-code-deletions:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeDeletionsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-admin-invitation-email-conflicts
test-scenario-admin-invitation-email-conflicts:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAdminInvitationEmailConflictsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-auth-lifecycle
test-scenario-auth-lifecycle:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAuthLifecycleAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-household-delete-pin
test-scenario-household-delete-pin:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredHouseholdDeletePINAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-admin-invitation-lifecycle
test-scenario-admin-invitation-lifecycle:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAdminInvitationLifecycleAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-household-update
test-scenario-household-update:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredHouseholdUpdateAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-password-sessions
test-scenario-password-sessions:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredPasswordSessionsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invitation-token-lifecycle
test-scenario-invitation-token-lifecycle:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInvitationTokenLifecycleAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-household-create
test-scenario-household-create:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredHouseholdCreateAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-device-decisions
test-scenario-device-decisions:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredDeviceDecisionsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-onboarding-reads
test-scenario-onboarding-reads:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredOnboardingReadsAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-device-removal
test-scenario-device-removal:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredDeviceRemovalAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-onboarding-progress
test-scenario-onboarding-progress:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredOnboardingProgressAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-avatar
test-scenario-avatar:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredAvatarAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invite-code-creation
test-scenario-invite-code-creation:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInviteCodeCreationAcceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-login-r1
test-scenario-login-r1:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredLoginR1Acceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-signup-family
test-scenario-signup-family:
	SILO_SCENARIO_REQUIRED=1 go test ./internal/scenariocatalog/executor -run '^TestRequiredSignupFamilyAcceptance$$' -count=1 -v

.PHONY: test-scenario-device-poll-r1
test-scenario-device-poll-r1:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredDevicePollR1Acceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-invitation-r1
test-scenario-invitation-r1:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredInvitationR1Acceptance$$' ./internal/scenariocatalog/executor

.PHONY: test-scenario-resources-handoff-impersonation
test-scenario-resources-handoff-impersonation:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredResourcesHandoffImpersonationAcceptance$$' ./internal/scenariocatalog/executor

# Three frozen local hardware inventory reads and the non-admin refusal shape; probes host ffmpeg.
.PHONY: test-scenario-hardware-inventory
test-scenario-hardware-inventory:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredHardwareInventoryAcceptance$$' ./internal/scenariocatalog/executor

# Eight frozen plugin launch cases: four cookie issuances and four refusals; no plugin is served.
.PHONY: test-scenario-plugin-launch
test-scenario-plugin-launch:
	SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredPluginLaunchAcceptance$$' ./internal/scenariocatalog/executor
