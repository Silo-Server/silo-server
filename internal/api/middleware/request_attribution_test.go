package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"testing"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

func attributionLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	t.Setenv("SILO_QUERY_ATTRIBUTION_ENABLED", "true")
	if !queryattr.Initialize() {
		t.Fatal("query attribution is disabled")
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func TestRequestLoggerAddsDatabaseFields(t *testing.T) {
	logs := attributionLogs(t)
	var queryLine int
	h := RequestLogger("test-node")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, line, _ := runtime.Caller(0)
		op := queryattr.Begin(r.Context(), queryattr.KindQuery, "SELECT 1", nil, 1)
		queryLine = line + 1
		op.End(r.Context(), nil)
		w.WriteHeader(http.StatusNoContent)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/items", nil))
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["msg"] != "api request" || record["db_queries"] != float64(1) {
		t.Errorf("request msg=%v db_queries=%v, want api request and 1", record["msg"], record["db_queries"])
	}
	wantSite := fmt.Sprintf("api/middleware.TestRequestLoggerAddsDatabaseFields.func1:%d", queryLine)
	if record["db_slowest_site"] != wantSite {
		t.Errorf("db_slowest_site = %v, want %s", record["db_slowest_site"], wantSite)
	}
	if shape, ok := record["db_slowest_shape"].(string); !ok || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(shape) {
		t.Errorf("db_slowest_shape = %v, want 16 lowercase hex digits", record["db_slowest_shape"])
	}
	if record["db_top_count"] != float64(1) {
		t.Errorf("db_top_count = %v, want 1", record["db_top_count"])
	}
}

func TestRequestLoggerNoQuery(t *testing.T) {
	logs := attributionLogs(t)
	h := RequestLogger("test-node")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/items", nil))
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"db_slowest_site", "db_slowest_shape", "db_top_site", "db_top_shape"} {
		if value, present := record[key]; present {
			t.Errorf("%s = %v, want field absent", key, value)
		}
	}
	for _, key := range []string{"db_queries", "db_query_ns", "db_acquire_wait_ns", "db_slowest_ns", "db_top_count"} {
		if value, present := record[key]; present {
			t.Errorf("%s = %v, want field absent", key, value)
		}
	}
}
