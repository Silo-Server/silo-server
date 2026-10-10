# The Go jobs' lanes. CI runs this file from the trusted CI tools checkout in
# the repository root: Go lint runs lint and then contracts, and Go test runs
# tests, each job on its own runner.
# LINT_BASE_REF, LINT_BASE_MODE, CONTRACT_BASE_REF, and TEST_GOFLAGS come from
# the workflow.
.PHONY: lint contracts tests

# Router recovery must hold over the whole tree, including inherited lines.
lint:
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "::error::gofmt is required on:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	BASE_REF="$${LINT_BASE_REF}" LINT_CHANGED_CI=1 LINT_CHANGED_BASE_MODE="$${LINT_BASE_MODE}" CI_OPERATION=lint bash .ci-tools/scripts/lint-changed.sh
	$(MAKE) -f Makefile lint-router-recovery

# Package assertions belong to the tests lane; these check generated artifacts.
contracts:
	$(MAKE) -j1 -f Makefile verify-settings-bindings verify-playback-fixtures verify-route-inventory
	$(MAKE) -j1 -f Makefile verify-migration-ledger verify-apiv2-openapi CONTRACT_GO_TESTS=0
	git fetch --no-tags origin "+refs/heads/$${CONTRACT_BASE_REF}:refs/remotes/origin/$${CONTRACT_BASE_REF}"
	$(MAKE) -j1 -f Makefile verify-apiv2-contract CONTRACT_GO_TESTS=0 BASE_REF="origin/$${CONTRACT_BASE_REF}"

# Go's test cache cannot see files a test's child process reads, so these
# packages always rerun; the grep fails when a new test starts go, git, or node
# outside them or the packages whose subprocess inputs are immutable.
UNCACHED := cmd/silo internal/jellycompat internal/pluginhost internal/plugins
IMMUTABLE := internal/contractledger internal/contractspec internal/debugserver
tests:
	GOFLAGS="$${TEST_GOFLAGS}" $(MAKE) -f Makefile test-go
	@want="$$(printf '%s\n' $(UNCACHED) $(IMMUTABLE) | sort -u)"; \
	got="$$(git grep -l -E '(exec\.Command(Context)?\(([^,]+, )?"(go|git|node)"|LookPath\("(go|git|node)"\))' -- '*_test.go' | sed 's@/[^/]*$$@@' | sort -u)"; \
	if [ "$$want" != "$$got" ]; then echo "::error::packages whose tests start go, git, or node changed"; echo "want: $$want"; echo "got: $$got"; exit 1; fi
	go test -count=1 $(addprefix ./,$(UNCACHED))
