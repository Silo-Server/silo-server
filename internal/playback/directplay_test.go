package playback

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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

	serve := func(method, rangeHeader, ifMatch, ifRange, ifNoneMatch string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/stream", nil)
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
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

	full := serve(http.MethodGet, "", "", "", "")
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
		rr := serve(http.MethodHead, "", "", "", "")
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
			rr := serve(http.MethodGet, tt.rangeHeader, "", "", "")
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
		rr := serve(http.MethodGet, "bytes=7-", "", etag, "")
		if rr.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", rr.Code)
		}
		if body := rr.Body.String(); body != content[7:] {
			t.Fatalf("body = %q, want %q", body, content[7:])
		}
	})

	t.Run("stale If-Range", func(t *testing.T) {
		rr := serve(http.MethodGet, "bytes=7-", "", "\"stale\"", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if body := rr.Body.String(); body != content {
			t.Fatalf("body = %q, want full entity %q", body, content)
		}
	})

	t.Run("bounded range with matching If-Match", func(t *testing.T) {
		if !validatorRequired {
			t.Skip("platform does not expose a durable file revision")
		}
		rr := serve(http.MethodGet, "bytes=7-11", etag, "", "")
		if rr.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", rr.Code)
		}
		if body := rr.Body.String(); body != content[7:12] {
			t.Fatalf("body = %q, want %q", body, content[7:12])
		}
	})

	t.Run("bounded range with stale If-Match", func(t *testing.T) {
		rr := serve(http.MethodGet, "bytes=7-11", "\"stale\"", "", "")
		if rr.Code != http.StatusPreconditionFailed {
			t.Fatalf("status = %d, want 412", rr.Code)
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("body length = %d, want 0", rr.Body.Len())
		}
	})

	t.Run("If-None-Match", func(t *testing.T) {
		if !validatorRequired {
			t.Skip("platform does not expose a durable file revision")
		}
		rr := serve(http.MethodGet, "", "", "", etag)
		if rr.Code != http.StatusNotModified {
			t.Fatalf("status = %d, want 304", rr.Code)
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("body length = %d, want 0", rr.Body.Len())
		}
	})
}

func TestDirectStreamConditionalResult(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		hadIfMatch    bool
		hadIfRange    bool
		ifRangeResult string
		want          string
	}{
		{name: "unconditional", status: http.StatusOK, want: directStreamConditionalNone},
		{name: "If-Match passed", status: http.StatusPartialContent, hadIfMatch: true, want: directStreamConditionalIfMatchPassed},
		{name: "If-Match failed", status: http.StatusPreconditionFailed, hadIfMatch: true, want: directStreamConditionalIfMatchFailed},
		{name: "If-Range matched", status: http.StatusPartialContent, hadIfRange: true, ifRangeResult: directStreamConditionalIfRangeMatched, want: directStreamConditionalIfRangeMatched},
		{name: "If-Range mismatched", status: http.StatusOK, hadIfRange: true, ifRangeResult: directStreamConditionalIfRangeMismatched, want: directStreamConditionalIfRangeMismatched},
		{name: "If-Range not evaluated after 304", status: http.StatusNotModified, hadIfRange: true, ifRangeResult: directStreamConditionalIfRangeMatched, want: directStreamConditionalIfRangeNotEvaluated},
		{name: "If-Range not evaluated after 412", status: http.StatusPreconditionFailed, hadIfRange: true, ifRangeResult: directStreamConditionalIfRangeMismatched, want: directStreamConditionalIfRangeNotEvaluated},
		{name: "If-Range without Range", status: http.StatusOK, hadIfRange: true, want: directStreamConditionalIfRangeNotEvaluated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := directStreamConditionalResult(tt.status, tt.hadIfMatch, tt.hadIfRange, tt.ifRangeResult); got != tt.want {
				t.Fatalf("conditional result = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDirectStreamIfRangeResult(t *testing.T) {
	const currentETag = `"current"`
	modtime := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		method    string
		rangeHead string
		validator string
		modtime   time.Time
		want      string
	}{
		{name: "matching ETag", method: http.MethodGet, rangeHead: "bytes=2-4", validator: currentETag, modtime: modtime, want: directStreamConditionalIfRangeMatched},
		{name: "matching ETag with whitespace", method: http.MethodGet, rangeHead: "bytes=2-4", validator: `  "current"  `, modtime: modtime, want: directStreamConditionalIfRangeMatched},
		{name: "stale ETag", method: http.MethodGet, rangeHead: "bytes=2-4", validator: `"stale"`, modtime: modtime, want: directStreamConditionalIfRangeMismatched},
		{name: "weak ETag", method: http.MethodGet, rangeHead: "bytes=2-4", validator: `W/"current"`, modtime: modtime, want: directStreamConditionalIfRangeMismatched},
		{name: "matching date", method: http.MethodHead, rangeHead: "bytes=2-4", validator: modtime.Format(http.TimeFormat), modtime: modtime, want: directStreamConditionalIfRangeMatched},
		{name: "stale date", method: http.MethodGet, rangeHead: "bytes=2-4", validator: modtime.Add(-time.Second).Format(http.TimeFormat), modtime: modtime, want: directStreamConditionalIfRangeMismatched},
		{name: "missing range", method: http.MethodGet, validator: currentETag, modtime: modtime},
		{name: "empty validator", method: http.MethodGet, rangeHead: "bytes=2-4", modtime: modtime},
		{name: "unsupported method", method: http.MethodPost, rangeHead: "bytes=2-4", validator: currentETag, modtime: modtime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/stream", nil)
			if tt.rangeHead != "" {
				req.Header.Set("Range", tt.rangeHead)
			}
			if tt.validator != "" {
				req.Header.Set("If-Range", tt.validator)
			}
			if got := directStreamIfRangeResult(req, currentETag, tt.modtime); got != tt.want {
				t.Fatalf("If-Range result = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("uses first If-Range header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/stream", nil)
		req.Header.Set("Range", "bytes=2-4")
		req.Header["If-Range"] = []string{currentETag, `"stale"`}
		if got := directStreamIfRangeResult(req, currentETag, modtime); got != directStreamConditionalIfRangeMatched {
			t.Fatalf("If-Range result = %q, want %q", got, directStreamConditionalIfRangeMatched)
		}
	})
}

func TestDirectStreamValidatorFingerprint(t *testing.T) {
	const validator = "\"private-validator\""
	fingerprint := directStreamValidatorFingerprint(validator)
	if len(fingerprint) != 16 {
		t.Fatalf("fingerprint length = %d, want 16", len(fingerprint))
	}
	if strings.Contains(fingerprint, "private") || strings.Contains(fingerprint, "validator") {
		t.Fatalf("fingerprint leaked validator text: %q", fingerprint)
	}
	if got := directStreamValidatorFingerprint(validator); got != fingerprint {
		t.Fatalf("fingerprint = %q, want stable %q", got, fingerprint)
	}
	if got := directStreamValidatorFingerprint("\"different\""); got == fingerprint {
		t.Fatalf("different validator produced the same fingerprint %q", got)
	}
	if got := directStreamValidatorFingerprint("  "); got != "" {
		t.Fatalf("blank validator fingerprint = %q, want omitted", got)
	}
}

func TestServeDirectPlayConditionalDiagnostics(t *testing.T) {
	if !platformRequiresDirectPlayValidator() {
		t.Skip("platform does not expose a durable file revision")
	}

	filePath := filepath.Join(t.TempDir(), "private-fixture.mp4")
	if err := os.WriteFile(filePath, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	initial := httptest.NewRecorder()
	if err := ServeDirectPlay(initial, httptest.NewRequest(http.MethodGet, "/stream", nil), filePath); err != nil {
		t.Fatal(err)
	}
	currentETag := initial.Header().Get("ETag")
	if currentETag == "" {
		t.Fatal("initial response omitted ETag")
	}

	tests := []struct {
		name            string
		rangeHeader     string
		headerName      string
		validator       string
		wantStatus      int
		wantResult      string
		fingerprintName string
	}{
		{
			name:            "If-Match mismatch",
			rangeHeader:     "bytes=2-4",
			headerName:      "If-Match",
			validator:       "\"stale-private-if-match\"",
			wantStatus:      http.StatusPreconditionFailed,
			wantResult:      directStreamConditionalIfMatchFailed,
			fingerprintName: "if_match_fingerprint",
		},
		{
			name:            "If-Range mismatch",
			rangeHeader:     "bytes=2-4",
			headerName:      "If-Range",
			validator:       "\"stale-private-if-range\"",
			wantStatus:      http.StatusOK,
			wantResult:      directStreamConditionalIfRangeMismatched,
			fingerprintName: "if_range_fingerprint",
		},
		{
			name:            "If-Range match with unsatisfiable range",
			rangeHeader:     "bytes=999-1000",
			headerName:      "If-Range",
			validator:       currentETag,
			wantStatus:      http.StatusRequestedRangeNotSatisfiable,
			wantResult:      directStreamConditionalIfRangeMatched,
			fingerprintName: "if_range_fingerprint",
		},
		{
			name:            "If-Range match with ignored aggregate ranges",
			rangeHeader:     "bytes=0-9,0-9",
			headerName:      "If-Range",
			validator:       currentETag,
			wantStatus:      http.StatusOK,
			wantResult:      directStreamConditionalIfRangeMatched,
			fingerprintName: "if_range_fingerprint",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs.Reset()
			req := httptest.NewRequest(http.MethodGet, "/stream", nil)
			req.Header.Set("Range", tt.rangeHeader)
			req.Header.Set(tt.headerName, tt.validator)
			rr := httptest.NewRecorder()
			if err := ServeDirectPlay(rr, req, filePath); err != nil {
				t.Fatal(err)
			}
			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tt.wantStatus)
			}

			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatalf("decode structured log %q: %v", logs.String(), err)
			}
			if got := record["conditional_result"]; got != tt.wantResult {
				t.Fatalf("conditional_result = %v, want %q", got, tt.wantResult)
			}
			if got := record["had_if_match"]; got != (tt.headerName == "If-Match") {
				t.Fatalf("had_if_match = %v, want %v", got, tt.headerName == "If-Match")
			}
			if got := record[tt.fingerprintName]; got != directStreamValidatorFingerprint(tt.validator) {
				t.Fatalf("%s = %v, want request fingerprint", tt.fingerprintName, got)
			}
			responseETag := rr.Header().Get("ETag")
			if tt.wantStatus == http.StatusRequestedRangeNotSatisfiable {
				// ServeContent strips ETag from its 416 response, but the end log
				// still fingerprints the validator used for If-Range evaluation.
				responseETag = currentETag
			}
			if got := record["etag_fingerprint"]; got != directStreamValidatorFingerprint(responseETag) {
				t.Fatalf("etag_fingerprint = %v, want response ETag fingerprint", got)
			}
			if strings.Contains(logs.String(), strings.Trim(tt.validator, "\"")) {
				t.Fatalf("structured log leaked raw request validator: %s", logs.String())
			}
			if strings.Contains(logs.String(), strings.Trim(responseETag, "\"")) {
				t.Fatalf("structured log leaked raw response validator: %s", logs.String())
			}
			if strings.Contains(logs.String(), filePath) {
				t.Fatalf("structured log leaked media path: %s", logs.String())
			}
		})
	}
}

func TestServeDirectPlayChangedEntityRejectsOldValidators(t *testing.T) {
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

	// Size and mtime are pinned identical on purpose, so the inode change time
	// is the only thing left that can distinguish the two entities. Linux
	// stamps ctime from a coarse clock — the whole rewrite finishes inside one
	// tick — so writing once and reading immediately usually produces the same
	// ctime and the test fails for a reason that has nothing to do with the
	// validator. Rewrite until the stamp actually moves.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := os.WriteFile(filePath, []byte(replacement), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filePath, originalTime, originalTime); err != nil {
			t.Fatal(err)
		}
		probe := httptest.NewRecorder()
		if err := ServeDirectPlay(probe, httptest.NewRequest(http.MethodGet, "/stream", nil), filePath); err != nil {
			t.Fatal(err)
		}
		if probe.Header().Get("ETag") != oldETag {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("file revision never changed across a replacement; the platform exposes no usable validator")
		}
		time.Sleep(time.Millisecond)
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

	ifMatchRequest := httptest.NewRequest(http.MethodGet, "/stream", nil)
	ifMatchRequest.Header.Set("Range", "bytes=5-8")
	ifMatchRequest.Header.Set("If-Match", oldETag)
	ifMatchResponse := httptest.NewRecorder()
	if err := ServeDirectPlay(ifMatchResponse, ifMatchRequest, filePath); err != nil {
		t.Fatal(err)
	}
	if ifMatchResponse.Code != http.StatusPreconditionFailed {
		t.Fatalf("If-Match status = %d, want 412", ifMatchResponse.Code)
	}
	if ifMatchResponse.Body.Len() != 0 {
		t.Fatalf("If-Match body length = %d, want 0", ifMatchResponse.Body.Len())
	}
	if newETag := ifMatchResponse.Header().Get("ETag"); newETag == oldETag {
		t.Fatalf("If-Match response did not expose the replacement ETag: %q", newETag)
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
