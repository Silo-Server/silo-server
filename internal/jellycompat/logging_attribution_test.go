package jellycompat

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

func TestRequestLoggerLogsDatabaseFields(t *testing.T) {
	t.Setenv("SILO_QUERY_ATTRIBUTION_ENABLED", "true")
	if !queryattr.Initialize() {
		t.Fatal("query attribution is disabled")
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	h := requestLoggerMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := queryattr.Begin(r.Context(), queryattr.KindQuery, "SELECT 1", nil, 1)
		op.End(r.Context(), nil)
		w.WriteHeader(http.StatusNoContent)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/Items", nil))
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["msg"] != "jellycompat request" || record["db_queries"] != float64(1) {
		t.Errorf("request msg=%v db_queries=%v, want jellycompat request and 1", record["msg"], record["db_queries"])
	}
	if site, ok := record["db_slowest_site"].(string); !ok || !strings.HasPrefix(site, "jellycompat.TestRequestLoggerLogsDatabaseFields.func2:") {
		t.Errorf("db_slowest_site = %v, want wrapped handler site", record["db_slowest_site"])
	}
}
