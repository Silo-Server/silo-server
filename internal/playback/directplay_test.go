package playback

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const (
	directPlayDarwinGOOS  = "darwin"
	directPlayLinuxGOOS   = "linux"
	directPlayWindowsGOOS = "windows"
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
	validatorRequired := platformRequiresDirectPlayValidator()
	if validatorRequired && etag == "" {
		t.Fatalf("ETag omitted on supported platform %s", runtime.GOOS)
	}
	if !validatorRequired && etag != "" {
		t.Fatalf("ETag = %q on unsupported platform %s, want omitted validator", etag, runtime.GOOS)
	}
	if etag != "" && (strings.HasPrefix(etag, "W/") || !strings.HasPrefix(etag, "\"") || !strings.HasSuffix(etag, "\"")) {
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
		wantStart   int64
	}{
		{
			name:        "bounded range",
			rangeHeader: "bytes=5-9",
			wantStatus:  http.StatusPartialContent,
			wantRange:   fmt.Sprintf("bytes 5-9/%d", len(content)),
			wantBody:    content[5:10],
			wantStart:   5,
		},
		{
			name:        "suffix range",
			rangeHeader: "bytes=-4",
			wantStatus:  http.StatusPartialContent,
			wantRange:   fmt.Sprintf("bytes %d-%d/%d", len(content)-4, len(content)-1, len(content)),
			wantBody:    content[len(content)-4:],
			wantStart:   int64(len(content) - 4),
		},
		{
			name:        "open ended range",
			rangeHeader: "bytes=10-",
			wantStatus:  http.StatusPartialContent,
			wantRange:   fmt.Sprintf("bytes 10-%d/%d", len(content)-1, len(content)),
			wantBody:    content[10:],
			wantStart:   10,
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
			resumesBefore := counterValue(t, directStreamRangeResumes)
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
			if tt.wantStatus == http.StatusPartialContent {
				if got := directStreamRangeStart(rr.Code, rr.Header().Get("Content-Range")); got != tt.wantStart {
					t.Fatalf("range start = %d, want %d", got, tt.wantStart)
				}
				if got := counterValue(t, directStreamRangeResumes); got != resumesBefore+1 {
					t.Fatalf("resume counter = %v, want %v", got, resumesBefore+1)
				}
			}
		})
	}

	t.Run("matching If-Range", func(t *testing.T) {
		if !validatorRequired {
			t.Skip("platform does not expose a durable file revision")
		}
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
		if !validatorRequired {
			t.Skip("platform does not expose a durable file revision")
		}
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
	if !platformRequiresDirectPlayValidator() {
		t.Skip("platform does not expose a durable file revision")
	}

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
	if oldETag == "" {
		t.Fatalf("ETag omitted on supported platform %s", runtime.GOOS)
	}

	const replacement = "replaced bytes"
	if len(replacement) != len(original) {
		t.Fatal("test fixture must preserve file size")
	}
	if err := os.WriteFile(filePath, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filePath, originalTime, originalTime); err != nil {
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

func TestDirectPlayEntityTagOmitsUnsupportedRevision(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "fixture.mp4")
	if err := os.WriteFile(filePath, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close fixture: %v", err)
		}
	})

	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if got := directPlayEntityTag(file, fileInfoWithoutSystem{FileInfo: info}); got != "" {
		t.Fatalf("ETag without durable revision = %q, want omitted validator", got)
	}
}

type fileInfoWithoutSystem struct {
	os.FileInfo
}

func (fileInfoWithoutSystem) Sys() any {
	return nil
}

func platformRequiresDirectPlayValidator() bool {
	switch runtime.GOOS {
	case directPlayDarwinGOOS, directPlayLinuxGOOS, directPlayWindowsGOOS:
		return true
	default:
		return false
	}
}

func TestServeDirectPlayStalledWriteIncrementsOutcomeMetric(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "fixture.mp4")
	if err := os.WriteFile(filePath, []byte("media bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	stalledEnds := directStreamEnds.WithLabelValues(string(httpstream.OutcomeStalledReap))
	endsBefore := counterValue(t, stalledEnds)
	activeBefore := gaugeValue(t, directStreamActive)

	writer := &deadlineResponseWriter{header: make(http.Header)}
	if err := ServeDirectPlay(writer, httptest.NewRequest(http.MethodGet, "/stream", nil), filePath); err != nil {
		t.Fatal(err)
	}

	if writer.status != http.StatusOK {
		t.Fatalf("status = %d, want %d", writer.status, http.StatusOK)
	}
	if got := counterValue(t, stalledEnds); got != endsBefore+1 {
		t.Fatalf("stalled end counter = %v, want %v", got, endsBefore+1)
	}
	if got := gaugeValue(t, directStreamActive); got != activeBefore {
		t.Fatalf("active stream gauge = %v, want restored value %v", got, activeBefore)
	}
}

func counterValue(t testing.TB, counter prometheus.Counter) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := counter.Write(metric); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return metric.GetCounter().GetValue()
}

func gaugeValue(t testing.TB, gauge prometheus.Gauge) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := gauge.Write(metric); err != nil {
		t.Fatalf("read gauge: %v", err)
	}
	return metric.GetGauge().GetValue()
}

type deadlineResponseWriter struct {
	header http.Header
	status int
}

func (w *deadlineResponseWriter) Header() http.Header {
	return w.header
}

func (w *deadlineResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *deadlineResponseWriter) Write([]byte) (int, error) {
	return 0, os.ErrDeadlineExceeded
}
