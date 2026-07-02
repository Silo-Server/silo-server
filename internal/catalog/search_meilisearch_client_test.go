package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
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
