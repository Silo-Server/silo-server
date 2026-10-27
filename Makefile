.PHONY: frontend build dev-frontend dev-backend dev-proxy dev-transcode lint lint-changed test test-go test-db-pins test-web embed-stub clean jellyfin-web verify-local-paths verify-case-collisions install-hooks migrate-create migrate-validate migrate-status migrate-up migrate-down-to settings-bindings verify-settings-bindings verify-settings-bindings-web verify-settings-bindings-all playback-fixtures verify-playback-fixtures route-inventory verify-route-inventory lint-router-recovery apiv2-openapi verify-apiv2-openapi verify-apiv2-contract apiv2-fixtures verify-apiv2-fixtures apiv2-fixtures-sync verify-apiv2-fixtures-siblings apiv2-web-types verify-apiv2-web-types

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
JELLYFIN_WEB_VERSION ?= 12.1

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
	SILO_SHUTDOWN_DELAY=$${SILO_SHUTDOWN_DELAY:-0} go run ./cmd/silo/

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

# Lint changed lines in the Go packages this branch touched.
# BASE_REF=origin/<pr-base> when not main.
lint-changed:
	BASE_REF=$(BASE_REF) scripts/lint-changed.sh

.PHONY: verify-actionlint verify-shellcheck
verify-actionlint:
	@test "$$(actionlint -version | head -n 1)" = 1.7.12 || { echo 'actionlint 1.7.12 is required' >&2; exit 1; }
	actionlint

verify-shellcheck:
	@test "$$(shellcheck --version | sed -n 's/^version: //p')" = 0.11.0 || { echo 'ShellCheck 0.11.0 is required' >&2; exit 1; }
	@python3 -c 'import pathlib, re, subprocess; paths = subprocess.check_output(["git", "ls-files", "-z"]).decode().split("\0"); files = [p for p in paths if p and pathlib.Path(p).is_file() and (pathlib.Path(p).suffix in (".sh", ".bash", ".ksh") or re.match(rb"^#!.*\b(?:sh|bash|dash|ksh)\b", pathlib.Path(p).open("rb").readline()))]; assert files, "no tracked shell inputs"; subprocess.run(["shellcheck", "--", *files], check=True)'

# hadolint: not measured; adoption needs a pinned binary, a seeded-defect trial, and reviewed findings and cost.
# Makefile shell lint: not measured; recipe expansion needs a prototype that preserves Make's shell context.

.PHONY: verify-size-ratchet verify-bidi
verify-size-ratchet:
	@base=$$(git merge-base "$(BASE_REF)" HEAD) && \
		go run ./scripts/ci/sizeratchet -base "$$base"

# grep status 1 means no controls; unsupported PCRE and other errors refuse.
verify-bidi:
	@status=0; git grep -I -nP '[\x{061c}\x{200e}\x{200f}\x{202a}-\x{202e}\x{2066}-\x{2069}]' -- . || status=$$?; \
		if [ "$$status" -eq 1 ]; then exit 0; fi; \
		if [ "$$status" -eq 0 ]; then echo 'Bidi controls are not permitted in tracked text' >&2; exit 1; fi; \
		exit "$$status"

# Frontend test files that fail on main today. This list is shrink-only: delete
# an entry along with its fix, and never extend it to land a change. The Go
# suite has no equivalent — a Go test that cannot pass yet carries a t.Skip and
# its reason in the source, where whoever reads the test finds it.
WEBTEST_KNOWN_FAILURES :=

# The Go binary embeds the built frontend, so every Go build and test needs
# web/dist to exist. Tests never serve it, so a placeholder is enough; `make
# build` still builds the real bundle.
embed-stub:
	@mkdir -p web/dist
	@[ -e web/dist/index.html ] || printf '<!doctype html>\n' > web/dist/index.html

# Run the Go and frontend test suites.
test: test-go test-web

# The queryattr concurrency tests prove nothing without the race detector.
test-go: embed-stub
	go test ./...
	go test -race -count=1 ./internal/database/queryattr/

# Run the DB-backed query-budget pins listed in $(DB_PINS), then the database
# contracts, against the migrated, disposable database named by
# SILO_TEST_DATABASE_URL. Unlike test-go, a listed test that skips (no URL,
# unmigrated schema) or is missing fails the run. Migrate a fresh database first with
# DATABASE_URL=<url> SECRET_KEY=<32+ chars> go run ./cmd/silo/ --migrate-only.
DB_PINS := scripts/ci/db-pins.txt
test-db-pins: embed-stub
	go run ./scripts/ci/dbpins -list $(DB_PINS)
	$(MAKE) test-db-contracts

.PHONY: test-db-contracts
# Existing database boundary tests own assertions retired from unit tests.
# Use the same runner so a missing or skipped keeper fails validation.
test-db-contracts: embed-stub
	SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL="$${SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL:-$$SILO_TEST_DATABASE_URL}" \
		go run ./scripts/ci/dbpins -list scripts/ci/db-contracts.txt

# WEBTEST_ARGS passes extra vitest flags through; CI uses it to shard the
# suite across runners (--shard=N/M).
WEBTEST_ARGS ?=
test-web:
	cd web && pnpm exec vitest run $(WEBTEST_KNOWN_FAILURES) $(WEBTEST_ARGS)

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
PLAYBACK_WIRE_FIXTURES := start_request.json replan_request.json decision_response.json native-decision_response.json hls-subtitle-rendition-decision_response.json capability_response.json error_response.json route_event.json buffering-start_request.json buffering-replan_request.json learned-limit-decision_response.json plan-summary-route_event.json

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

# Run only the route inventory's router-recovery rule over the whole tree.
# Tests are excluded from this rule, so do not analyze their package variants.
lint-router-recovery:
	golangci-lint run --config .golangci-router-recovery.yml --tests=false --max-same-issues=0 --max-issues-per-linter=0 --uniq-by-line=false ./...

# CI gives test-go ownership of these Go assertions and passes 0 to avoid
# repeating them in the artifact job. Local verify targets remain complete.
# Older Makefiles ignore the variable and safely run the assertions again.
CONTRACT_GO_TESTS ?= 1

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
ifneq ($(CONTRACT_GO_TESTS),0)
	@go test -count=1 ./internal/contractspec/ \
		|| { echo "::error::$(APIV2_OPENAPI) fails the spec lint or the diff tool no longer detects the seeded breaking fixture"; exit 1; }
endif
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

# Check committed content for local machine path leaks.
verify-local-paths:
	scripts/check-local-path-leaks.sh

# Fail on tracked paths that name the same file on a case-insensitive file
# system (macOS, Windows), including JS/TS modules that differ only in case
# once the extension an import omits is removed.
verify-case-collisions:
	go run ./scripts/ci/casecollisions

# Create a timestamped Goose SQL migration. Usage: make migrate-create NAME=add_thing
migrate-create:
	@if [ -z "$(NAME)" ]; then echo "usage: make migrate-create NAME=add_thing"; exit 1; fi
	$(GOOSE) -dir $(GOOSE_DIR) create "$(NAME)" sql

# Validate Goose migration annotations and SQL parsing without touching a database.
migrate-validate:
	$(GOOSE) -dir $(GOOSE_DIR) validate

.PHONY: verify-migrations verify-prometheus-rules
verify-migrations: migrate-validate
	@base=$$(git merge-base "$(BASE_REF)" HEAD) && \
		go run ./scripts/ci/migrationcheck -base "$$base"

# Use the same Prometheus release locally and in CI.
PROMTOOL ?= docker run --rm --pull=never --entrypoint promtool -v "$(CURDIR)/deploy/observability:/rules:ro" -w /rules prom/prometheus:v3.5.0
verify-prometheus-rules:
	$(PROMTOOL) test rules silo.rules.test.yml

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
