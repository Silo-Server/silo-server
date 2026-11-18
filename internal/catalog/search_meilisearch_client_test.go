package catalog

import (
	"context"

	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newMeilisearchTestClient returns a client for an httptest server running handler.
func newMeilisearchTestClient(t *testing.T, handler http.HandlerFunc) *meilisearchClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := newMeilisearchClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("newMeilisearchClient: %v", err)
	}
	return client
}

func TestMeilisearchWaitTaskSurfacesTaskFailure(t *testing.T) {
	client := newMeilisearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"taskUid":7,"status":"failed","error":{"message":"document is malformed","code":"invalid_document"}}`))
	})
	err := client.WaitTask(context.Background(), newMeilisearchTaskRef(7))
	if err == nil || !strings.Contains(err.Error(), "document is malformed") {
		t.Fatalf("WaitTask error = %v, want task failure with meilisearch message", err)
	}
}

func TestMeilisearchClientJoinsBasePathAndSendsAuth(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"available"}`))
	}))
	defer server.Close()

	client, err := newMeilisearchClient(server.URL+"/meili", "secret-key", time.Second)
	if err != nil {
		t.Fatalf("newMeilisearchClient: %v", err)
	}
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if gotPath != "/meili/health" {
		t.Fatalf("request path = %q, want /meili/health", gotPath)
	}
	if gotAuth != "Bearer secret-key" {
		t.Fatalf("Authorization = %q, want Bearer secret-key", gotAuth)
	}
}
