package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// A retryable subtitle answer keeps its 503 + Retry-After shape and adds, not
// changes, what it tells the client: the covered range and, when the extraction
// knows one, a more specific reason.
func TestWriteSubtitleRetryKeepsTheRetryableShape(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set(playback.SubtitleCoverageHeader, "600.000-720.000")
	rec.Header().Set(playback.SubtitleWindowedHeader, "true")
	writeSubtitleRetry(rec, &playback.SubtitleExtractionRetryError{RetryAfter: 1500 * time.Millisecond})

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want the delay rounded up to 2", got)
	}
	// The requested window is not what was produced, so it must not stay on the
	// response as if it were.
	if got := rec.Header().Get(playback.SubtitleCoverageHeader); got != "" {
		t.Fatalf("coverage = %q, want none: the page was not produced", got)
	}
	if got := rec.Header().Get(playback.SubtitleWindowedHeader); got != "" {
		t.Fatalf("windowed = %q, want none", got)
	}
	if !strings.Contains(rec.Body.String(), `"error":"subtitle_source_unavailable"`) {
		t.Fatalf("body = %q, want the existing problem code", rec.Body.String())
	}
}

// A page refused for its read cost reports its own code, and a page the
// progressive artifact has partly parsed advertises that range.
func TestWriteSubtitleRetryReportsCostAndCoverage(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSubtitleRetry(rec, &playback.SubtitleExtractionRetryError{
		RetryAfter:   2 * time.Second,
		Reason:       playback.SubtitlePageTooCostlyReason,
		CoveredUntil: 200,
	})

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get(playback.SubtitleCoverageHeader); got != "0.000-200.000" {
		t.Fatalf("coverage = %q, want the parsed range 0.000-200.000", got)
	}
	if got := rec.Header().Get(playback.SubtitleWindowedHeader); got != "true" {
		t.Fatalf("windowed = %q, want true with a coverage range", got)
	}
	if !strings.Contains(rec.Body.String(), `"error":"`+playback.SubtitlePageTooCostlyReason+`"`) {
		t.Fatalf("body = %q, want the page-cost problem code", rec.Body.String())
	}
}

// A page the track's progressive artifact has not parsed yet is retryable too,
// and names that condition: the source is fine, the page is just not on disk
// yet. It still advertises how far the artifact has parsed.
func TestWriteSubtitleRetryReportsAPageNotParsedYet(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSubtitleRetry(rec, &playback.SubtitleExtractionRetryError{
		RetryAfter:   2 * time.Second,
		Reason:       playback.SubtitlePageNotReadyReason,
		CoveredUntil: 126.543,
	})

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}
	if got := rec.Header().Get(playback.SubtitleCoverageHeader); got != "0.000-126.543" {
		t.Fatalf("coverage = %q, want the parsed range 0.000-126.543", got)
	}
	if !strings.Contains(rec.Body.String(), `"error":"`+playback.SubtitlePageNotReadyReason+`"`) {
		t.Fatalf("body = %q, want the page-not-ready problem code", rec.Body.String())
	}
}

// An extraction that produced nothing must be told apart by cause, and never
// answered as a plain failure when the cause is the source: the production run
// turned a 120 s window extract timeout into a non-retryable 500, so the client
// spent its four attempts and gave up on the track.
func TestWriteSubtitleExtractFailureClassifiesByCause(t *testing.T) {
	timeout := context.DeadlineExceeded
	upstream := errors.New("ffmpeg subtitle stream failed: exit status 1 (stderr: Server returned 503 Service Unavailable)")
	relayToken := errors.New("ffmpeg subtitle stream failed: exit status 1 (stderr: Server returned 404 Not Found) open http://127.0.0.1:9000/source/abc/stream.mkv")
	broken := errors.New("ffmpeg subtitle stream failed: exit status 1 (stderr: Invalid data found when processing input)")

	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "extract deadline", err: timeout, wantStatus: http.StatusServiceUnavailable, wantCode: "subtitle_source_unavailable"},
		{name: "upstream 5xx", err: upstream, wantStatus: http.StatusServiceUnavailable, wantCode: "subtitle_source_unavailable"},
		{name: "relay token lapsed", err: relayToken, wantStatus: http.StatusServiceUnavailable, wantCode: "subtitle_source_unavailable"},
		{name: "real extraction failure", err: broken, wantStatus: http.StatusInternalServerError, wantCode: "subtitle_extract_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rec.Header().Set(playback.SubtitleCoverageHeader, "600.000-720.000")
			writeSubtitleExtractFailure(rec, tc.err, "http://127.0.0.1:9000/source/abc/stream.mkv")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), `"error":"`+tc.wantCode+`"`) {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantCode)
			}
			if got := rec.Header().Get(playback.SubtitleCoverageHeader); got != "" {
				t.Fatalf("coverage = %q, want none on a failed extract", got)
			}
			if tc.wantStatus == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
				t.Fatal("a retryable answer carries no Retry-After")
			}
		})
	}
}
