package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestLegacyMetricsUseRegisteredRoutesAndBoundMethods(t *testing.T) {
	router := chi.NewRouter()
	router.Use(Metrics)
	router.Get("/files/{name}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	before := testutil.ToFloat64(httpRequestsTotal.WithLabelValues("GET", "/files/{name}", "204"))
	beforeUnknown := testutil.ToFloat64(httpRequestsTotal.WithLabelValues("other", "unmatched", "405"))
	for i := 0; i < 100; i++ {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", fmt.Sprintf("/files/private-name-%d", i), nil))
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(fmt.Sprintf("CUSTOM%d", i), "/files/secret", nil))
	}
	if after := testutil.ToFloat64(httpRequestsTotal.WithLabelValues("GET", "/files/{name}", "204")); after-before != 100 {
		t.Fatalf("registered route counter %v -> %v", before, after)
	}
	if after := testutil.ToFloat64(httpRequestsTotal.WithLabelValues("other", "unmatched", "405")); after-beforeUnknown != 100 {
		t.Fatalf("method folding %v -> %v", beforeUnknown, after)
	}
}
