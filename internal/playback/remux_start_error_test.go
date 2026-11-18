package playback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// failoverResponseWriter mirrors the v2 stream response writer's commitment
// rules: the first status sticks, a 4xx/5xx locks the response into its
// rejected state, and body bytes after that are discarded. A byte handler's
// failover can only reach the client if the failed attempt left this writer
// untouched.
type failoverResponseWriter struct {
	header   http.Header
	status   int
	rejected bool
	body     bytes.Buffer
}

func (w *failoverResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *failoverResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if status >= 400 {
		w.rejected = true
	}
}

func (w *failoverResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.rejected {
		return len(p), nil
	}
	return w.body.Write(p)
}

// remuxFakeFFmpeg writes a fake ffmpeg that answers the dovi_rpu capability
// probe and then runs body for the real remux invocation.
func remuxFakeFFmpeg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$arg\" = \"-bsfs\" ]; then exit 0; fi\n" +
		"done\n" +
		body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

// A remux that exits before producing a single byte must not commit a status
// when the caller owns the response: committing one locks the v2 writer into
// its rejected state and discards the sibling candidate's body.
func TestServeRemuxPreBodyNoOutputDefersResponse(t *testing.T) {
	source := remuxSourceFile(t)
	w := &failoverResponseWriter{}
	request := httptest.NewRequest(http.MethodGet, "/api/v2/stream/session-1", nil)

	err := ServeRemuxWithOptions(w, request, source, "mp4", 0, false, 0, 0, RemuxServeOptions{
		FFmpegPath:      remuxFakeFFmpeg(t, "exit 1\n"),
		DeferStartError: true,
	})
	if err == nil {
		t.Fatal("a remux that produced no output returned no error")
	}
	if !errors.Is(err, errRemuxNoOutput) {
		t.Fatalf("error = %v, want it to identify the no-output failure", err)
	}
	if err == errRemuxNoOutput { //nolint:errorlint // identity check: the error must wrap the sentinel, not equal it
		t.Fatalf("error = %v, want the underlying read cause wrapped alongside it", err)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("error = %v, want the original read cause preserved", err)
	}
	if w.status != 0 {
		t.Fatalf("pre-body failure committed status %d", w.status)
	}
	if w.rejected {
		t.Fatalf("pre-body failure locked the response into its rejected state")
	}
	if w.body.Len() != 0 {
		t.Fatalf("pre-body failure wrote body %q", w.body.String())
	}
}

// The caller can fail over to a sibling after a deferred failure: because the
// first attempt left the response uncommitted, the retry's 200 and bytes reach
// the client instead of being discarded.
func TestServeRemuxPreBodyFailureAllowsSiblingFailover(t *testing.T) {
	source := remuxSourceFile(t)
	w := &failoverResponseWriter{}
	request := httptest.NewRequest(http.MethodGet, "/api/v2/stream/session-1", nil)

	firstErr := ServeRemuxWithOptions(w, request, source, "mp4", 0, false, 0, 0, RemuxServeOptions{
		FFmpegPath:      remuxFakeFFmpeg(t, "exit 1\n"),
		DeferStartError: true,
	})
	if firstErr == nil {
		t.Fatal("the first candidate returned no error")
	}
	if w.status != 0 || w.rejected {
		t.Fatalf("first candidate committed status=%d rejected=%v before the failover", w.status, w.rejected)
	}

	// The byte handler, having failed the first candidate, does not write the
	// error: it retries a sibling on the same response.
	retryErr := ServeRemuxWithOptions(w, request, source, "mp4", 0, false, 0, 0, RemuxServeOptions{
		FFmpegPath:      remuxFakeFFmpeg(t, "printf 'sibling-bytes'; exit 0\n"),
		DeferStartError: true,
	})
	if retryErr != nil {
		t.Fatalf("sibling remux = %v, want success", retryErr)
	}
	if w.status != http.StatusOK {
		t.Fatalf("sibling status = %d, want 200", w.status)
	}
	if w.body.String() != "sibling-bytes" {
		t.Fatalf("body = %q, want the sibling candidate's bytes", w.body.String())
	}
}

// The start-failure site (ffmpeg could not even be spawned) defers too, and
// keeps the spawn cause in the returned error.
func TestServeRemuxSpawnFailureDefersResponse(t *testing.T) {
	source := remuxSourceFile(t)
	w := &failoverResponseWriter{}
	request := httptest.NewRequest(http.MethodGet, "/api/v2/stream/session-1", nil)

	err := ServeRemuxWithOptions(w, request, source, "mp4", 0, false, 0, 0, RemuxServeOptions{
		FFmpegPath:      filepath.Join(t.TempDir(), "does-not-exist"),
		DeferStartError: true,
	})
	if err == nil {
		t.Fatal("a remux that failed to spawn returned no error")
	}
	if !strings.Contains(err.Error(), "start ffmpeg") {
		t.Fatalf("error = %v, want the spawn cause preserved", err)
	}
	if w.status != 0 || w.rejected || w.body.Len() != 0 {
		t.Fatalf("spawn failure committed status=%d rejected=%v body=%q", w.status, w.rejected, w.body.String())
	}
}

// Callers that cannot fail over (the proxy and transcode node relays) keep the
// historical behavior: the transport writes its own error.
func TestServeRemuxPreBodyFailureWritesErrorWhenNotDeferred(t *testing.T) {
	source := remuxSourceFile(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/stream/session-1", nil)

	err := ServeRemuxWithOptions(recorder, request, source, "mp4", 0, false, 0, 0, RemuxServeOptions{
		FFmpegPath: remuxFakeFFmpeg(t, "exit 1\n"),
	})
	if err == nil {
		t.Fatal("a remux that produced no output returned no error")
	}
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for a non-deferring caller", recorder.Code)
	}
}

// A relay that accepts the request but never produces a media byte must not
// park the response until the client stops the session (a dead virtual candidate
// hung a production media request for 38.9s). The first-output window closes the
// process and returns the same no-output error a fast failure produces, so the
// caller can rotate candidates or fail fast. The window is the ordinary budget
// plus one grace for a still-silent process (see remuxFirstOutputSlowSourceGrace),
// and it stays finite, which is the invariant here.
func TestServeRemuxFirstOutputTimeoutFailsFast(t *testing.T) {
	prevGrace := remuxFirstOutputSlowSourceGrace
	remuxFirstOutputSlowSourceGrace = 200 * time.Millisecond
	t.Cleanup(func() { remuxFirstOutputSlowSourceGrace = prevGrace })

	source := remuxSourceFile(t)
	w := &failoverResponseWriter{}
	request := httptest.NewRequest(http.MethodGet, "/api/v2/stream/session-1", nil)

	started := time.Now()
	err := ServeRemuxWithOptions(w, request, source, "mp4", 0, false, 0, 0, RemuxServeOptions{
		FFmpegPath:         remuxFakeFFmpeg(t, "exec sleep 30\n"),
		DeferStartError:    true,
		FirstOutputTimeout: 100 * time.Millisecond,
	})
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a remux that never produced a byte returned no error")
	}
	if !errors.Is(err, errRemuxNoOutput) {
		t.Fatalf("error = %v, want it to identify the no-output failure", err)
	}
	if !strings.Contains(err.Error(), "first output timeout after 300ms") {
		t.Fatalf("error = %v, want the ordinary window and its grace named", err)
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("first-output timeout took %s; the silent source was indicted before its extension was spent", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("first-output timeout took %s, want a bounded failure", elapsed)
	}
	if w.status != 0 || w.rejected || w.body.Len() != 0 {
		t.Fatalf("timeout committed status=%d rejected=%v body=%q", w.status, w.rejected, w.body.String())
	}
}

// Once streaming has begun a later failure must still terminate the response
// cleanly: the 200 and the bytes already produced stay, and the call returns
// without surfacing an error that a caller could mistake for a pre-body one.
func TestServeRemuxMidStreamFailureKeepsCommittedResponse(t *testing.T) {
	source := remuxSourceFile(t)
	w := &failoverResponseWriter{}
	request := httptest.NewRequest(http.MethodGet, "/api/v2/stream/session-1", nil)

	err := ServeRemuxWithOptions(w, request, source, "mp4", 0, false, 0, 0, RemuxServeOptions{
		FFmpegPath:      remuxFakeFFmpeg(t, "printf 'early-bytes'; exit 1\n"),
		DeferStartError: true,
	})
	if err != nil {
		t.Fatalf("mid-stream failure = %v, want a terminated response with no error", err)
	}
	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want the committed 200", w.status)
	}
	if w.body.String() != "early-bytes" {
		t.Fatalf("body = %q, want the bytes written before the failure", w.body.String())
	}
	if w.rejected {
		t.Fatalf("mid-stream failure was rewritten into a rejected response")
	}
}

// A remux that produced no output must say why. FFmpeg's stderr is the only
// place the reason appears, and a production failure (session
// a88e6d0c-2edd-463b-a052-e19c41bf6ebc) reached the log as a bare EOF because
// the transport never captured it.
func TestServeRemuxNoOutputLogsFFmpegStderr(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	err := ServeRemuxWithOptions(&failoverResponseWriter{}, httptest.NewRequest(http.MethodGet, "/api/v2/stream/session-1", nil),
		"virtual://movie/tt11378946?result=cand-a", "mp4", 0, false, 0, 0, RemuxServeOptions{
			FFmpegPath:      remuxFakeFFmpeg(t, "echo 'Protocol not found' >&2\nexit 1\n"),
			DeferStartError: true,
		})
	if !errors.Is(err, errRemuxNoOutput) {
		t.Fatalf("error = %v, want the no-output failure", err)
	}
	line := logs.String()
	for _, want := range []string{"remux produced no output", "input_kind", "virtual", "Protocol not found"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log = %s, want it to contain %q", line, want)
		}
	}
}

// TestRemuxNoOutputDowngradesClientTeardown proves a remux whose read ends
// because the client left is recorded at debug, while the same failure with the
// request still live stays a warning. Every production instance of the warning
// was co-timed to a client cancel after the session had already delivered
// media, so the warn level told operators nothing about the encoder.
func TestRemuxNoOutputDowngradesClientTeardown(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	session := &RemuxSession{stderr: &remuxStderrTail{}}
	_, _ = session.stderr.Write([]byte("Error opening input file"))

	logRemuxNoOutput(context.Background(), "/media/movie.mkv", session)
	if line := logs.String(); !strings.Contains(line, `"level":"WARN"`) || !strings.Contains(line, "Error opening input file") {
		t.Fatalf("live-request log = %s, want the encoder warning with FFmpeg stderr", line)
	}

	logs.Reset()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	logRemuxNoOutput(canceled, "/media/movie.mkv", session)
	line := logs.String()
	if strings.Contains(line, `"level":"WARN"`) {
		t.Fatalf("client-cancel log = %s, want no encoder warning", line)
	}
	if !strings.Contains(line, `"level":"DEBUG"`) || !strings.Contains(line, `"reason":"client_canceled"`) {
		t.Fatalf("client-cancel log = %s, want the debug teardown line", line)
	}
}
