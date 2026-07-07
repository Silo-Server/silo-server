package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMeilisearchWaitTaskAcceptsZeroTaskUID(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"taskUid":0,"status":"succeeded"}`))
	}))
	defer server.Close()

	client, err := newMeilisearchClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("newMeilisearchClient returned error: %v", err)
	}
	if err := client.WaitTask(context.Background(), newMeilisearchTaskRef(0)); err != nil {
		t.Fatalf("WaitTask(0) returned error: %v", err)
	}
	if gotPath != "/tasks/0" {
		t.Fatalf("WaitTask requested path %q, want /tasks/0", gotPath)
	}
}

func TestMeilisearchWaitTaskNoopsWithoutTask(t *testing.T) {
	if err := (&meilisearchClient{}).WaitTask(context.Background(), meilisearchTaskRef{}); err != nil {
		t.Fatalf("WaitTask(no task) returned error: %v", err)
	}
}

func TestMeilisearchWaitTaskSurfacesTaskFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"taskUid":7,"status":"failed","error":{"message":"document is malformed","code":"invalid_document"}}`))
	}))
	defer server.Close()

	client, err := newMeilisearchClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("newMeilisearchClient: %v", err)
	}
	err = client.WaitTask(context.Background(), newMeilisearchTaskRef(7))
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

func TestMeilisearchClientDecodesJSONErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"invalid filter expression","code":"invalid_search_filter"}`))
	}))
	defer server.Close()

	client, err := newMeilisearchClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("newMeilisearchClient: %v", err)
	}
	_, err = client.Search(context.Background(), "idx", meilisearchSearchRequest{})
	var httpErr *meilisearchHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("Search error = %v, want *meilisearchHTTPError", err)
	}
	if httpErr.StatusCode != http.StatusBadRequest || httpErr.Message != "invalid filter expression" || httpErr.Code != "invalid_search_filter" {
		t.Fatalf("decoded error = %+v, want status 400 with message and code", httpErr)
	}
}

func TestMeilisearchClientKeepsNonJSONErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("  upstream proxy exploded\n"))
	}))
	defer server.Close()

	client, err := newMeilisearchClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("newMeilisearchClient: %v", err)
	}
	_, err = client.Stats(context.Background(), "idx")
	var httpErr *meilisearchHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("Stats error = %v, want *meilisearchHTTPError", err)
	}
	if httpErr.StatusCode != http.StatusInternalServerError || httpErr.Message != "upstream proxy exploded" {
		t.Fatalf("decoded error = %+v, want trimmed plain-text message", httpErr)
	}
}

func TestMeilisearchDeleteIndexToleratesMissingIndex(t *testing.T) {
	var gotMethod, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Index gone not found.","code":"index_not_found"}`))
	}))
	defer server.Close()

	client, err := newMeilisearchClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("newMeilisearchClient: %v", err)
	}
	ref, err := client.DeleteIndex(context.Background(), "gone")
	if err != nil {
		t.Fatalf("DeleteIndex of a missing index should be a no-op, got %v", err)
	}
	if ref.hasTask {
		t.Fatalf("missing index should not yield a task, got %+v", ref)
	}
	if gotMethod != http.MethodDelete || gotPath != "/indexes/gone" {
		t.Fatalf("unexpected request %s %s, want DELETE /indexes/gone", gotMethod, gotPath)
	}
}

func TestMeilisearchListIndexUIDsPaginates(t *testing.T) {
	var offsets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		offsets = append(offsets, offset)
		w.Header().Set("Content-Type", "application/json")
		switch offset {
		case "0":
			_, _ = w.Write([]byte(`{"results":[{"uid":"a"},{"uid":"b"}],"offset":0,"limit":2,"total":3}`))
		default:
			_, _ = fmt.Fprintf(w, `{"results":[{"uid":"c"}],"offset":%s,"limit":2,"total":3}`, offset)
		}
	}))
	defer server.Close()

	client, err := newMeilisearchClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("newMeilisearchClient: %v", err)
	}
	uids, err := client.ListIndexUIDs(context.Background())
	if err != nil {
		t.Fatalf("ListIndexUIDs: %v", err)
	}
	if len(uids) != 3 || uids[0] != "a" || uids[1] != "b" || uids[2] != "c" {
		t.Fatalf("uids = %v, want [a b c]", uids)
	}
	if len(offsets) != 2 || offsets[0] != "0" || offsets[1] != "2" {
		t.Fatalf("request offsets = %v, want [0 2]", offsets)
	}
}
