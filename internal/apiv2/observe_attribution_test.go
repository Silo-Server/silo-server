package apiv2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

func TestObserveLogsDatabaseFields(t *testing.T) {
	t.Setenv("SILO_QUERY_ATTRIBUTION_ENABLED", "true")
	if !queryattr.Initialize() {
		t.Fatal("query attribution is disabled")
	}
	logs := captureLogs(t)
	router := chi.NewRouter()
	router.Use(observe)
	router.Get("/api/v2/items", func(w http.ResponseWriter, r *http.Request) {
		op := queryattr.Begin(r.Context(), queryattr.KindQuery, "SELECT 1", nil, 1)
		op.End(r.Context(), nil)
		w.WriteHeader(http.StatusNoContent)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v2/items", nil))
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["msg"] != "apiv2 request" || record["db_queries"] != float64(1) {
		t.Errorf("request msg=%v db_queries=%v, want apiv2 request and 1", record["msg"], record["db_queries"])
	}
	if site, ok := record["db_slowest_site"].(string); !ok || !strings.HasPrefix(site, "apiv2.TestObserveLogsDatabaseFields.func1:") {
		t.Errorf("db_slowest_site = %v, want routed handler site", record["db_slowest_site"])
	}
}
