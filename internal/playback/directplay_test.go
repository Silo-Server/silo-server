package playback

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServeDirectPlayHTTPContract(t *testing.T) {
	const content = "0123456789abcdefghijklmnopqrstuvwxyz"
	filePath := filepath.Join(t.TempDir(), "fixture.mp4")
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	serve := func(method, rangeHeader, ifRange, ifNoneMatch string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/stream", nil)
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		if ifRange != "" {
			req.Header.Set("If-Range", ifRange)
		}
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rr := httptest.NewRecorder()
		if err := ServeDirectPlay(rr, req, filePath); err != nil {
			t.Fatalf("ServeDirectPlay: %v", err)
		}
		return rr
	}

	full := serve(http.MethodGet, "", "", "")
	if full.Code != http.StatusOK {
		t.Fatalf("full status = %d, want 200", full.Code)
	}
	if body := full.Body.String(); body != content {
		t.Fatalf("full body = %q, want %q", body, content)
	}
	if got := full.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}
	etag := full.Header().Get("ETag")
	if etag == "" || strings.HasPrefix(etag, "W/") || !strings.HasPrefix(etag, "\"") || !strings.HasSuffix(etag, "\"") {
		t.Fatalf("ETag = %q, want a strong quoted validator", etag)
	}

	t.Run("HEAD", func(t *testing.T) {
		rr := serve(http.MethodHead, "", "", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("body length = %d, want 0", rr.Body.Len())
		}
		if rr.Header().Get("ETag") != etag {
			t.Fatalf("ETag = %q, want %q", rr.Header().Get("ETag"), etag)
		}
		if rr.Header().Get("Accept-Ranges") != "bytes" {
			t.Fatalf("Accept-Ranges = %q, want bytes", rr.Header().Get("Accept-Ranges"))
		}
		if rr.Header().Get("Content-Length") != fmt.Sprint(len(content)) {
			t.Fatalf("Content-Length = %q, want %d", rr.Header().Get("Content-Length"), len(content))
		}
	})

	tests := []struct {
		name        string
		rangeHeader string
		wantStatus  int
		wantRange   string
		wantBody    string
	}{
		{
			name:        "bounded range",
			rangeHeader: "bytes=5-9",
			wantStatus:  http.StatusPartialContent,
			wantRange:   fmt.Sprintf("bytes 5-9/%d", len(content)),
			wantBody:    content[5:10],
		},
		{
			name:        "suffix range",
			rangeHeader: "bytes=-4",
			wantStatus:  http.StatusPartialContent,
			wantRange:   fmt.Sprintf("bytes %d-%d/%d", len(content)-4, len(content)-1, len(content)),
			wantBody:    content[len(content)-4:],
		},
		{
			name:        "open ended range",
			rangeHeader: "bytes=10-",
			wantStatus:  http.StatusPartialContent,
			wantRange:   fmt.Sprintf("bytes 10-%d/%d", len(content)-1, len(content)),
			wantBody:    content[10:],
		},
		{
			name:        "syntactically invalid range",
			rangeHeader: "bytes=invalid",
			wantStatus:  http.StatusRequestedRangeNotSatisfiable,
			wantRange:   fmt.Sprintf("bytes */%d", len(content)),
		},
		{
			name:        "unsatisfiable range",
			rangeHeader: "bytes=999-1000",
			wantStatus:  http.StatusRequestedRangeNotSatisfiable,
			wantRange:   fmt.Sprintf("bytes */%d", len(content)),
		},
		{
			name:        "range starts at EOF",
			rangeHeader: fmt.Sprintf("bytes=%d-", len(content)),
			wantStatus:  http.StatusRequestedRangeNotSatisfiable,
			wantRange:   fmt.Sprintf("bytes */%d", len(content)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := serve(http.MethodGet, tt.rangeHeader, "", "")
			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %q", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if got := rr.Header().Get("Content-Range"); got != tt.wantRange {
				t.Fatalf("Content-Range = %q, want %q", got, tt.wantRange)
			}
			if tt.wantStatus == http.StatusPartialContent && rr.Body.String() != tt.wantBody {
				t.Fatalf("body = %q, want %q", rr.Body.String(), tt.wantBody)
			}
		})
	}

	t.Run("matching If-Range", func(t *testing.T) {
		rr := serve(http.MethodGet, "bytes=7-", etag, "")
		if rr.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", rr.Code)
		}
		if body := rr.Body.String(); body != content[7:] {
			t.Fatalf("body = %q, want %q", body, content[7:])
		}
	})

	t.Run("stale If-Range", func(t *testing.T) {
		rr := serve(http.MethodGet, "bytes=7-", "\"stale\"", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if body := rr.Body.String(); body != content {
			t.Fatalf("body = %q, want full entity %q", body, content)
		}
	})

	t.Run("If-None-Match", func(t *testing.T) {
		rr := serve(http.MethodGet, "", "", etag)
		if rr.Code != http.StatusNotModified {
			t.Fatalf("status = %d, want 304", rr.Code)
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("body length = %d, want 0", rr.Body.Len())
		}
	})
}

func TestServeDirectPlayChangedEntityRejectsOldIfRange(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "fixture.mp4")
	const original = "original bytes"
	if err := os.WriteFile(filePath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	originalTime := time.Now().Add(-10 * time.Second).Truncate(time.Second)
	if err := os.Chtimes(filePath, originalTime, originalTime); err != nil {
		t.Fatal(err)
	}

	first := httptest.NewRecorder()
	if err := ServeDirectPlay(first, httptest.NewRequest(http.MethodGet, "/stream", nil), filePath); err != nil {
		t.Fatal(err)
	}
	oldETag := first.Header().Get("ETag")

	const replacement = "replacement entity with a different size"
	if err := os.WriteFile(filePath, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	replacementTime := originalTime.Add(5 * time.Second)
	if err := os.Chtimes(filePath, replacementTime, replacementTime); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	req.Header.Set("Range", "bytes=5-")
	req.Header.Set("If-Range", oldETag)
	rr := httptest.NewRecorder()
	if err := ServeDirectPlay(rr, req, filePath); err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if body, err := io.ReadAll(rr.Result().Body); err != nil || string(body) != replacement {
		t.Fatalf("body = %q, err = %v; want full replacement entity", body, err)
	}
	if newETag := rr.Header().Get("ETag"); newETag == oldETag {
		t.Fatalf("ETag did not change after replacement: %q", newETag)
	}
}
