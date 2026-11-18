package catalog

import (
	"context"
	"encoding/json"

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

func TestMeilisearchFederatedSearchUsesMultiSearchPagination(t *testing.T) {
	var gotPath string
	var gotRequest meilisearchFederatedSearchRequest
	client := newMeilisearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":[{"content_id":"episode-1"}],"offset":20,"limit":10,"estimatedTotalHits":1}`))
	})
	request := meilisearchFederatedSearchRequest{
		Federation: meilisearchFederationOptions{Offset: 20, Limit: 10},
		Queries: []meilisearchFederatedQuery{
			{IndexUID: "catalog-v3", Query: "Who Are You?"},
		},
	}
	response, err := client.FederatedSearch(context.Background(), request)
	if err != nil {
		t.Fatalf("FederatedSearch: %v", err)
	}
	if gotPath != "/multi-search" {
		t.Fatalf("request path = %q, want /multi-search", gotPath)
	}
	if gotRequest.Federation.Offset != 20 || gotRequest.Federation.Limit != 10 || len(gotRequest.Queries) != 1 {
		t.Fatalf("decoded request = %#v", gotRequest)
	}
	if len(response.Hits) != 1 || response.Hits[0].ContentID != "episode-1" {
		t.Fatalf("response = %#v", response)
	}
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
