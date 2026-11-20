package playback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// An oversized window is still streamed but must be marked truncated so the
// prefix is never stored and served as a complete last-good slice.
func TestSubtitleWindowCaptureMarksTruncation(t *testing.T) {
	var dst bytes.Buffer
	capture := &subtitleWindowCapture{dst: &dst, remaining: 4}
	if _, err := capture.Write([]byte("abcd")); err != nil {
		t.Fatal(err)
	}
	if capture.truncated {
		t.Fatal("a response that exactly fills the bound is not truncated")
	}
	if _, err := capture.Write([]byte("e")); err != nil {
		t.Fatal(err)
	}
	if !capture.truncated {
		t.Fatal("a response past the bound must be marked truncated")
	}
	if got := dst.String(); got != "abcde" {
		t.Fatalf("streamed bytes = %q, want the full response", got)
	}
	if got := capture.buf.String(); got != "abcd" {
		t.Fatalf("captured bytes = %q, want the bounded prefix", got)
	}
}

// A failed destination must not stop the shared capture: the bytes still
// belong to the window result, and later writes must not touch the dead
// response writer.
func TestSubtitleWindowCaptureKeepsCapturingAfterDestinationFailure(t *testing.T) {
	dst := &prefixErrorWriter{limit: 3, err: errors.New("broken pipe")}
	capture := &subtitleWindowCapture{dst: dst, remaining: 64}

	n, err := capture.Write([]byte("abcdef"))
	if n != 6 || err != nil {
		t.Fatalf("first write = (%d, %v), want (6, nil)", n, err)
	}
	n, err = capture.Write([]byte("ghi"))
	if n != 3 || err != nil {
		t.Fatalf("second write = (%d, %v), want (3, nil)", n, err)
	}
	if dst.writes != 1 {
		t.Fatalf("destination write attempts = %d, want 1", dst.writes)
	}
	if got := dst.body.String(); got != "abc" {
		t.Fatalf("destination body = %q, want the accepted prefix", got)
	}
	if got := capture.buf.String(); got != "abcdefghi" {
		t.Fatalf("captured body = %q, want the full window", got)
	}
}

// A leader whose response writer breaks mid-body must still commit the window
// so a coalesced follower can serve it. This is the disconnect path that
// previously returned a write error to copyAndFlush, killed ffmpeg, and left
// the follower waiting on a fill that never committed.
func TestServeSUPExtractWindowedLeaderWriteFailureStillCommits(t *testing.T) {
	c, source := newTestCache(t)
	opts := windowedSupOpts(source, 0, 300, 600)
	opts.DisableBackgroundWarm = true
	payload := string(pgsWindowPage)

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })

	var (
		mu    sync.Mutex
		calls int
	)
	extract := func(_ context.Context, extractOpts StreamExtractOpts) error {
		mu.Lock()
		calls++
		leader := calls == 1
		mu.Unlock()
		if leader {
			close(started)
			<-release
		}
		_, err := io.WriteString(extractOpts.Writer, payload)
		return err
	}

	leaderWriter := &prefixErrorWriter{limit: 3, err: errors.New("broken pipe")}
	leaderResult := make(chan error, 1)
	go func() {
		leaderResult <- c.ServeSUPExtract(
			leaderWriter,
			httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1", nil),
			opts,
			extract,
		)
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("leader extraction did not start")
	}
	followerRec := httptest.NewRecorder()
	followerResult := make(chan error, 1)
	go func() {
		followerResult <- c.ServeSUPExtract(
			followerRec,
			httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1", nil),
			opts,
			extract,
		)
	}()

	releaseOnce.Do(func() { close(release) })

	if err := <-leaderResult; err != nil {
		t.Fatalf("leader error = %v, want the window committed", err)
	}
	if err := <-followerResult; err != nil {
		t.Fatalf("follower error = %v", err)
	}
	if got := followerRec.Body.String(); got != payload {
		t.Fatalf("follower body = %q, want %q", got, payload)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1 coalesced extraction", calls)
	}
}

func windowResilienceOpts(source string) StreamExtractOpts {
	return StreamExtractOpts{
		InputPath:             source,
		SourceCodec:           "ass",
		TrackIndex:            0,
		SeekSeconds:           120,
		DurationSeconds:       600,
		WindowRequested:       true,
		DisableBackgroundWarm: true,
	}
}

func subtitleWindowRequest() *http.Request {
	return httptest.NewRequest(http.MethodGet, "/subtitle.ass?position=120&duration=600", nil)
}

func upstreamSubtitleError() error {
	return errors.New(`ffmpeg subtitle stream failed: exit status 8 (stderr: [http @ 0x1] Server returned 5XX Server Error reply)`)
}

// A repeated identical window request must serve the last-good copy instead of
// re-running ffmpeg, including after the provider starts failing with a 5xx.
func TestServeExtractWindowedRepeatServesLastGoodThroughUpstreamFailure(t *testing.T) {
	c, source := newTestCache(t)
	opts := windowResilienceOpts(source)
	//nolint:misspell // "Dialogue" is the ASS cue keyword, not prose.
	payload := "[Script Info]\nDialogue: 0,0:02:00.00,0:02:01.00,Default,,0,0,0,,hello\n"

	calls := 0
	extract := func(_ context.Context, o StreamExtractOpts) error {
		calls++
		_, err := io.WriteString(o.Writer, payload)
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		rec := httptest.NewRecorder()
		if err := c.ServeExtract(rec, subtitleWindowRequest(), opts, extract); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if rec.Body.String() != payload {
			t.Fatalf("attempt %d: body = %q, want cached window", attempt, rec.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1 for a repeated window", calls)
	}

	// The upstream now 5xxes; the exact repeat still serves the last-good copy.
	failing := func(context.Context, StreamExtractOpts) error {
		calls++
		return upstreamSubtitleError()
	}
	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, subtitleWindowRequest(), opts, failing); err != nil {
		t.Fatalf("cached serve during upstream failure: %v", err)
	}
	if rec.Body.String() != payload {
		t.Fatalf("cached body during upstream failure = %q", rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d after upstream failure, want the cached copy (still 1)", calls)
	}
}

// Without a cached copy, an upstream 5xx is retried a bounded number of times
// inside the request, then the identity enters a backoff so an immediate client
// retry does not open the provider again. A lapsed cooldown keeps the failure
// streak, so a sustained outage escalates 10→20→40→60s and then holds at the
// cap instead of restarting at the base cooldown.
func TestServeExtractWindowedUpstreamFailureBacksOff(t *testing.T) {
	c, source := newTestCache(t)
	c.extractRetryDelays = []time.Duration{time.Millisecond}
	opts := windowResilienceOpts(source)
	calls := 0
	extract := func(context.Context, StreamExtractOpts) error {
		calls++
		return upstreamSubtitleError()
	}
	wantCalls := 1 + len(c.extractRetryDelays)
	format := subtitleFormatASS

	cooldowns := []time.Duration{
		subtitleExtractionFailureBaseCooldown,
		2 * subtitleExtractionFailureBaseCooldown,
		4 * subtitleExtractionFailureBaseCooldown,
		subtitleExtractionFailureMaxCooldown,
		subtitleExtractionFailureMaxCooldown,
	}
	for i, wantCooldown := range cooldowns {
		if i > 0 {
			expireExtractionCooldown(t, c, opts, format)
		}
		if err := c.ServeExtract(httptest.NewRecorder(), subtitleWindowRequest(), opts, extract); !IsSubtitleUpstreamError(err) {
			t.Fatalf("attempt %d error = %v, want the upstream failure", i, err)
		}
		if calls != wantCalls*(i+1) {
			t.Fatalf("attempt %d extract calls = %d, want %d", i, calls, wantCalls*(i+1))
		}
		remaining, _, cooling := c.extractionCoolingDown(opts, format)
		if !cooling {
			t.Fatalf("attempt %d: no cooldown after the failure", i)
		}
		if remaining <= wantCooldown-time.Second || remaining > wantCooldown {
			t.Fatalf("attempt %d cooldown = %s, want %s", i, remaining, wantCooldown)
		}

		// An immediate retry inside the cooldown is answered from the backoff
		// without extracting.
		err := c.ServeExtract(httptest.NewRecorder(), subtitleWindowRequest(), opts, extract)
		var retry *SubtitleExtractionRetryError
		if !errors.As(err, &retry) {
			t.Fatalf("attempt %d retry error = %v, want SubtitleExtractionRetryError", i, err)
		}
		if retry.RetryAfter <= wantCooldown-time.Second || retry.RetryAfter > wantCooldown {
			t.Fatalf("attempt %d retry after = %s, want %s", i, retry.RetryAfter, wantCooldown)
		}
		if calls != wantCalls*(i+1) {
			t.Fatalf("attempt %d extract calls = %d after a retry inside the backoff, want %d", i, calls, wantCalls*(i+1))
		}
	}

	// A successful extraction clears the streak, so the next failure restarts
	// at the base cooldown.
	c.noteExtractionSuccess(opts, format)
	c.noteExtractionFailure(opts, format)
	remaining, _, cooling := c.extractionCoolingDown(opts, format)
	if !cooling || remaining <= subtitleExtractionFailureBaseCooldown-time.Second || remaining > subtitleExtractionFailureBaseCooldown {
		t.Fatalf("cooldown after success = %s (cooling=%t), want %s", remaining, cooling, subtitleExtractionFailureBaseCooldown)
	}
}

// expireExtractionCooldown moves an identity's retry time into the past while
// leaving its failure streak intact, as if the cooldown had simply elapsed.
func expireExtractionCooldown(t *testing.T, c *SubtitleCache, opts StreamExtractOpts, format string) {
	t.Helper()
	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
	if !ok {
		t.Fatal("failed to compute the extraction key")
	}
	c.failureMu.Lock()
	defer c.failureMu.Unlock()
	guard, ok := c.failureGuard[key]
	if !ok {
		t.Fatal("no failure guard to expire")
	}
	guard.retryAt = time.Now().Add(-time.Second)
	c.failureGuard[key] = guard
}

// A cached window write that fails after committing 200 and a partial body must
// terminate the request: it is not a cache miss, so the caller must not serve
// the slice again or start an extraction against the same response.
func TestServeExtractWindowCacheWriteFailureTerminatesRequest(t *testing.T) {
	c, source := newTestCache(t)
	opts := windowResilienceOpts(source)
	//nolint:misspell // "Dialogue" is the ASS cue keyword, not prose.
	payload := "[Script Info]\nDialogue: 0,0:02:00.00,0:02:01.00,Default,,0,0,0,,hello\n"

	calls := 0
	extract := func(_ context.Context, o StreamExtractOpts) error {
		calls++
		_, err := io.WriteString(o.Writer, payload)
		return err
	}
	// Populate the last-good window cache.
	seed := httptest.NewRecorder()
	if err := c.ServeExtract(seed, subtitleWindowRequest(), opts, extract); err != nil {
		t.Fatalf("seed request: %v", err)
	}
	if calls != 1 {
		t.Fatalf("seed extract calls = %d, want 1", calls)
	}

	writeErr := errors.New("client write failed")
	writer := &prefixErrorWriter{limit: 8, err: writeErr}
	err := c.ServeExtract(writer, subtitleWindowRequest(), opts, extract)
	if !errors.Is(err, writeErr) {
		t.Fatalf("cached serve error = %v, want the write failure", err)
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d after the cached write failure, want no extraction", calls)
	}
	if writer.writes != 1 {
		t.Fatalf("cached serve write attempts = %d, want exactly 1 (no second serve)", writer.writes)
	}
	if writer.body.String() != payload[:8] {
		t.Fatalf("cached serve body = %q, want the accepted prefix %q", writer.body.String(), payload[:8])
	}
}

// prefixErrorWriter accepts limit bytes of the first write and then reports a
// write error, simulating a response whose client goes away mid-body.
type prefixErrorWriter struct {
	header http.Header
	status int
	limit  int
	err    error
	writes int
	body   bytes.Buffer
}

func (w *prefixErrorWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *prefixErrorWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *prefixErrorWriter) Write(p []byte) (int, error) {
	w.writes++
	take := w.limit
	if take > len(p) {
		take = len(p)
	}
	w.body.Write(p[:take])
	if take < len(p) {
		return take, w.err
	}
	return take, nil
}

// Concurrent identical window requests must coalesce onto one extraction and
// all serve its bytes.
func TestServeExtractWindowedCoalescesConcurrentRepeats(t *testing.T) {
	c, source := newTestCache(t)
	opts := windowResilienceOpts(source)
	payload := assWindowCue

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })

	var (
		mu    sync.Mutex
		calls int
	)
	extract := func(_ context.Context, o StreamExtractOpts) error {
		mu.Lock()
		calls++
		leader := calls == 1
		mu.Unlock()
		if leader {
			close(started)
			<-release
		}
		_, err := io.WriteString(o.Writer, payload)
		return err
	}

	type result struct {
		body string
		err  error
	}
	const requests = 4
	begin := make(chan struct{})
	results := make(chan result, requests)
	for i := 0; i < requests; i++ {
		go func() {
			<-begin
			rec := httptest.NewRecorder()
			err := c.ServeExtract(rec, subtitleWindowRequest(), opts, extract)
			results <- result{body: rec.Body.String(), err: err}
		}()
	}
	close(begin)

	// The leader holds the extraction open; the others either join its flight
	// or, if they arrive after it stores the slice, serve that stored copy.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("coalesced window extract did not start")
	}
	releaseOnce.Do(func() { close(release) })

	for i := 0; i < requests; i++ {
		got := <-results
		if got.err != nil {
			t.Fatalf("request %d: %v", i, got.err)
		}
		if got.body != payload {
			t.Fatalf("request %d body = %q, want %q", i, got.body, payload)
		}
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1 coalesced extraction", calls)
	}
}

// A bounded fresh-source retry replaces the failed relay URL but keeps the
// credential-free cache identity. The failed attempt's per-identity backoff
// suppresses ordinary client retries; it must not suppress that one deliberate
// re-open, and a success must clear the streak.
func TestServeExtractFreshSourceRetryBypassesIdentityBackoff(t *testing.T) {
	c, source := newTestCache(t)
	c.extractRetryDelays = []time.Duration{}
	opts := windowResilienceOpts(source)
	opts.CacheIdentity = "virtual-fresh-source"
	const payload = "[Script Info]\nDialogue: 0,0:02:00.00,0:02:01.00,Default,,0,0,0,,hello\n" //nolint:misspell // ASS cue keyword.
	const freshPath = "/relay/fresh-token"

	var calls []string
	extract := func(_ context.Context, o StreamExtractOpts) error {
		calls = append(calls, o.InputPath)
		if o.InputPath == opts.InputPath {
			return upstreamSubtitleError()
		}
		_, err := io.WriteString(o.Writer, payload)
		return err
	}
	format := subtitleFormatASS

	if err := c.ServeExtract(httptest.NewRecorder(), subtitleWindowRequest(), opts, extract); !IsSubtitleUpstreamError(err) {
		t.Fatalf("first extraction error = %v, want the upstream failure", err)
	}
	// An ordinary retry inside the cooldown must not open the provider again.
	var retry *SubtitleExtractionRetryError
	if err := c.ServeExtract(httptest.NewRecorder(), subtitleWindowRequest(), opts, extract); !errors.As(err, &retry) {
		t.Fatalf("retry inside backoff error = %v, want SubtitleExtractionRetryError", err)
	}
	if len(calls) != 1 {
		t.Fatalf("extract calls = %d after a retry inside the backoff, want 1", len(calls))
	}

	freshOpts := opts
	freshOpts.InputPath = freshPath
	freshOpts.FreshSourceRetry = true
	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, subtitleWindowRequest(), freshOpts, extract); err != nil {
		t.Fatalf("fresh-source retry: %v", err)
	}
	if rec.Body.String() != payload {
		t.Fatalf("fresh-source body = %q, want %q", rec.Body.String(), payload)
	}
	if len(calls) != 2 || calls[1] != freshPath {
		t.Fatalf("extract calls = %v, want the failed URL then %q", calls, freshPath)
	}
	if _, _, cooling := c.extractionCoolingDown(freshOpts, format); cooling {
		t.Fatal("fresh-source success left the identity cooling down")
	}
}
