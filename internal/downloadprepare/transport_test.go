package downloadprepare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPPreparerSendsAuthenticatedRecipe(t *testing.T) {
	var got Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/downloads/prepare" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer secret" {
			t.Fatalf("Authorization = %q", auth)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	want := Request{JobID: "job-1", InputPath: "/media/movie.mkv", OutputPath: "/artifacts/movie.mp4", TargetCodecVideo: "h264", TargetCodecAudio: "aac"}
	if err := (HTTPPreparer{}).Prepare(context.Background(), server.URL+"/", "secret", want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("request = %+v, want %+v", got, want)
	}
}

func TestHTTPPreparerReportsNodeFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "mount unavailable", http.StatusUnprocessableEntity)
	}))
	defer server.Close()

	err := (HTTPPreparer{}).Prepare(context.Background(), server.URL, "secret", Request{JobID: "job-2"})
	if err == nil {
		t.Fatal("expected remote failure")
	}
}
