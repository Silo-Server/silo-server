package policy

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
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

func TestVendorQualityTableMatchesAccessPackage(t *testing.T) {
	source := readVendorFile(t, "lib/quality.rego")
	for _, entry := range []struct {
		quality string
		rank    string
	}{
		{quality: `""`, rank: "0"},
		{quality: `"480P"`, rank: "1"},
		{quality: `"720P"`, rank: "2"},
		{quality: `"1080P"`, rank: "3"},
		{quality: `"2160P"`, rank: "4"},
		{quality: `"4320P"`, rank: "5"},
	} {
		needle := entry.quality + ": " + entry.rank
		if !strings.Contains(source, needle) {
			t.Fatalf("quality table missing %s", needle)
		}
	}
}

func TestVendorRatingTableMatchesAccessPackage(t *testing.T) {
	source := readVendorFile(t, "lib/ratings.rego")
	for _, entry := range access.RatingRankEntries() {
		needle := `"` + entry.Rating + `": ` + strconv.Itoa(entry.Rank)
		if !strings.Contains(source, needle) {
			t.Fatalf("rating table missing %s", needle)
		}
	}
}

func readVendorFile(t *testing.T, path string) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("vendor", path))
	if err != nil {
		t.Fatal(err)
	}
	return string(source)
}
