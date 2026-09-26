package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

func (f *fakeCatalog) SearchContinuationCapabilities(context.Context) (catalogpkg.SearchContinuationCapabilities, error) {
	return catalogpkg.SearchContinuationCapabilities{Provider: "meilisearch", ResultWindowLimit: 1000, SessionTTLSeconds: 900, MaxSessionsPerAccount: 16}, f.err
}

func TestCatalogSearchCapabilities(t *testing.T) {
	deps, _ := catalogActionDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/catalog/search/capabilities", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"result_window_limit":1000`) || !strings.Contains(rec.Body.String(), `"session_ttl_seconds":900`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"people_media_scope":true`) {
		t.Fatal("scoped people search is not advertised", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"person_prefetch":true`) {
		t.Fatal("person prefetch reads are not advertised", rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/search/capabilities", "", nil), TypeAuthenticationRequired)
	deps.People = nil
	rec = do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/catalog/search/capabilities", "", viewerHeaders())
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"people_media_scope":true`) || strings.Contains(rec.Body.String(), `"person_prefetch":true`) {
		t.Fatal("unwired people search must not be advertised", rec.Code, rec.Body.String())
	}
}
