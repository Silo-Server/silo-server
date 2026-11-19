package policy

import (
	"context"
	"strings"
	"testing"

	"github.com/open-policy-agent/opa/v1/storage"
	"github.com/open-policy-agent/opa/v1/tester"
)

func TestVendorRego(t *testing.T) {
	ctx := context.Background()
	modules, store, err := tester.Load([]string{"vendor"}, nil)
	if err != nil {
		t.Fatalf("load vendor rego: %v", err)
	}

	txn := storage.NewTransactionOrDie(ctx, store)
	defer store.Abort(ctx, txn)

	results, err := tester.NewRunner().
		SetStore(store).
		SetModules(modules).
		RunTests(ctx, txn)
	if err != nil {
		t.Fatalf("run vendor rego tests: %v", err)
	}

	var failed []string
	for result := range results {
		if !result.Pass() {
			failed = append(failed, result.String())
		}
	}
	if len(failed) > 0 {
		t.Fatalf("vendor rego tests failed:\n%s", strings.Join(failed, "\n"))
	}
}
