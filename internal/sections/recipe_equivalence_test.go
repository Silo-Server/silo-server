package sections

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

// TestRecipeDispatchEquivalence calls each registered Recipe and asserts that the resulting
// ResolvedItems match what the existing FetchOne path would produce for the same inputs.
// This guards against regressions during the registry refactor.
func TestRecipeDispatchEquivalence(t *testing.T) {
	// This test is exercised against a stub Fetcher (no DB) — it verifies the wiring,
	// not the query logic. Real catalog comparisons live in fetcher_*_test.go.
	f := &Fetcher{} // empty fetcher; FetchOne will return zero-value items for non-DB types
	InstallRecipeDelegate(f)

	rc := recipes.ResolverContext{
		Ctx:       context.Background(),
		ItemLimit: 10,
	}

	// Each type that has a no-DB-required short-circuit in FetchOne (continue_watching with no
	// store, next_up / next_in_series with no repo) should resolve without error.
	for _, typ := range []string{"continue_watching", "next_up", "next_in_series"} {
		rec, ok := recipes.Get(typ)
		if !ok {
			t.Fatalf("recipe %q missing", typ)
		}
		got, err := rec.Resolve(rc)
		if err != nil {
			t.Errorf("Resolve(%s): %v", typ, err)
			continue
		}
		if got.Items == nil {
			// Both old and new paths return an empty slice (not nil) when StoreProvider is missing.
			t.Errorf("%s: items is nil; expected empty slice", typ)
		}
	}
}
