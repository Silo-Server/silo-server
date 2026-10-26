package apiv2

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

var updateRecipeCatalogFixture = flag.Bool("update-recipe-catalog-fixture", false,
	"rewrite the web recipe catalog fixture from the real recipe registry")

// recipeCatalogFixture is the GET /api/v2/sections/recipes body the web tests
// read. It lives under web/ rather than testdata/ because the web payload,
// label and gate tests are what depend on it; keeping one copy for both trees
// means a recipe change cannot pass one side and drift on the other.
var recipeCatalogFixture = filepath.Join("..", "..", "web", "src", "lib", "homeRows", "__fixtures__", "recipeCatalog.v2.json")

// TestRecipeCatalogMatchesWebFixture fails when a recipe's preset key, type,
// category, default params or admin_only flag changes without the web
// fixture changing with it. The catalog takes no viewer, so one fixture
// covers every account. Run with -update-recipe-catalog-fixture to rewrite
// the fixture, and say in the pull request why each changed preset changed.
func TestRecipeCatalogMatchesWebFixture(t *testing.T) {
	deps, _ := homeDeps(t)
	deps.Recipes = &handlers.RecipeHandler{}
	rec := do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/sections/recipes", "", viewerHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v2/sections/recipes = %d: %s", rec.Code, rec.Body.String())
	}
	var got bytes.Buffer
	if err := json.Indent(&got, bytes.TrimSpace(rec.Body.Bytes()), "", "  "); err != nil {
		t.Fatalf("indent catalog: %v", err)
	}
	got.WriteByte('\n')

	if *updateRecipeCatalogFixture {
		if err := os.WriteFile(recipeCatalogFixture, got.Bytes(), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		return
	}
	want, err := os.ReadFile(recipeCatalogFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	// Compare values, not bytes, so reformatting the fixture cannot fail
	// the test without a catalog change.
	var gotValue, wantValue any
	if err := json.Unmarshal(got.Bytes(), &gotValue); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode fixture %s: %v", recipeCatalogFixture, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("the recipe catalog no longer matches %s; rerun with -update-recipe-catalog-fixture and review the diff", recipeCatalogFixture)
	}
}
