package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsHandlerServesPrometheus(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	newMetricsHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "# HELP ") {
		t.Fatal("metrics response did not contain Prometheus exposition data")
	}
}

func TestMetricsListenerDisabledByDefault(t *testing.T) {
	t.Setenv(metricsListenEnv, "")
	stop, err := startMetricsListener(true)
	if err != nil {
		t.Fatal(err)
	}
	stop()
}
