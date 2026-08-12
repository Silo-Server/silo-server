package downloadprepare

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
		_ = json.NewEncoder(w).Encode(Result{ArtifactID: got.ArtifactID, FileSize: 123})
	}))
	defer server.Close()

	want := Request{ArtifactID: "job-1", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac"}
	result, err := (HTTPPreparer{}).Prepare(context.Background(), server.URL+"/", "secret", want)
	if err != nil {
		t.Fatal(err)
	}
	if result != (Result{ArtifactID: "job-1", FileSize: 123}) {
		t.Fatalf("result = %+v", result)
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

	_, err := (HTTPPreparer{}).Prepare(context.Background(), server.URL, "secret", Request{ArtifactID: "job-2"})
	if err == nil {
		t.Fatal("expected remote failure")
	}
}

func TestHTTPPreparerManagesOpaqueArtifact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/downloads/artifacts/artifact-1" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Content-Length", "42")
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	defer server.Close()
	client := HTTPPreparer{}
	result, err := client.Stat(context.Background(), server.URL, "secret", "artifact-1")
	if err != nil || result != (Result{ArtifactID: "artifact-1", FileSize: 42}) {
		t.Fatalf("Stat = (%+v, %v)", result, err)
	}
	if err := client.Delete(context.Background(), server.URL, "secret", "artifact-1"); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPPreparerDeleteStopsStalledErrorBodyRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	started := time.Now()
	err := (HTTPPreparer{ReadIdleTimeout: 50 * time.Millisecond}).Delete(
		context.Background(), server.URL, "secret", "artifact-stalled-delete",
	)
	if !errors.Is(err, ErrRelayReadIdle) {
		t.Fatalf("Delete error = %v, want ErrRelayReadIdle", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled delete response took %s, want a bounded failure", elapsed)
	}
}

func TestHTTPPreparerRejectsArtifactPathTraversal(t *testing.T) {
	if _, err := (HTTPPreparer{}).Stat(context.Background(), "http://node", "secret", "../escape"); err == nil {
		t.Fatal("expected invalid artifact id error")
	}
}

func TestDefaultPrepareClientHasNoResponseHeaderDeadline(t *testing.T) {
	transport, ok := defaultPrepareHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", defaultPrepareHTTPClient.Transport)
	}
	if transport.ResponseHeaderTimeout != 0 {
		t.Fatalf("prepare response header timeout = %s, want none", transport.ResponseHeaderTimeout)
	}
}

func TestHTTPPreparerPrepareStopsStalledResultBodyRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	started := time.Now()
	_, err := (HTTPPreparer{ReadIdleTimeout: 50 * time.Millisecond}).Prepare(
		context.Background(), server.URL, "secret", Request{ArtifactID: "artifact-stalled-result"},
	)
	if !errors.Is(err, ErrRelayReadIdle) {
		t.Fatalf("Prepare error = %v, want ErrRelayReadIdle", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled result read took %s, want a bounded failure", elapsed)
	}
}

func TestRelayStatusAllowedPreservesServeContentOutcomes(t *testing.T) {
	for _, status := range []int{
		http.StatusOK,
		http.StatusPartialContent,
		http.StatusNotModified,
		http.StatusPreconditionFailed,
		http.StatusRequestedRangeNotSatisfiable,
	} {
		if !RelayStatusAllowed(status) {
			t.Errorf("status %d should be relayed", status)
		}
	}
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusBadRequest, http.StatusInternalServerError} {
		if RelayStatusAllowed(status) {
			t.Errorf("status %d should be rejected", status)
		}
	}
}

func TestCopyResponseHeadersDoesNotExposeOriginArtifactFilename(t *testing.T) {
	src := http.Header{
		"Content-Disposition": {`attachment; filename="opaque-artifact.mp4"`},
		"Content-Type":        {"video/mp4"},
		"Content-Length":      {"42"},
	}
	dst := make(http.Header)
	CopyResponseHeaders(dst, src)
	if got := dst.Get("Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition = %q, want omitted", got)
	}
	if dst.Get("Content-Type") != "video/mp4" || dst.Get("Content-Length") != "42" {
		t.Fatalf("copied headers = %v", dst)
	}
}

func TestHTTPPreparerOpenStopsStalledBodyRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	resp, err := (HTTPPreparer{ReadIdleTimeout: 50 * time.Millisecond}).Open(
		context.Background(), server.URL, "secret", "artifact-stalled", http.MethodGet, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	started := time.Now()
	_, err = io.ReadAll(resp.Body)
	if !errors.Is(err, ErrRelayReadIdle) {
		t.Fatalf("ReadAll error = %v, want ErrRelayReadIdle", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled read took %s, want a bounded failure", elapsed)
	}
}

func TestHTTPPreparerOpenAllowsContinuedBodyProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, _ := w.(http.Flusher)
		for _, chunk := range []string{"one", "two", "three"} {
			_, _ = io.WriteString(w, chunk)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer server.Close()

	resp, err := (HTTPPreparer{ReadIdleTimeout: 50 * time.Millisecond}).Open(
		context.Background(), server.URL, "secret", "artifact-progress", http.MethodGet, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "onetwothree" {
		t.Fatalf("body = %q", body)
	}
}
