package streamtelemetry

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

type readerFromRecorder struct {
	*httptest.ResponseRecorder
	readFrom bool
}

func (w *readerFromRecorder) ReadFrom(r io.Reader) (int64, error) {
	w.readFrom = true
	return io.Copy(w.ResponseRecorder, r)
}

func TestObservedWriterPreservesReadFromAndCountsAfterWrite(t *testing.T) {
	registry := NewRegistry(testConfig(), NewLocalStore(), nil)
	obs := registry.begin(testRoute(ClassPlayback), CaptureSet{})
	underlying := &readerFromRecorder{ResponseRecorder: httptest.NewRecorder()}
	w := &observedWriter{w: underlying, observation: obs, bodyEligible: true}
	n, err := w.ReadFrom(strings.NewReader("abcdef"))
	if err != nil || n != 6 || !underlying.readFrom || obs.BytesAccepted() != 6 {
		t.Fatalf("ReadFrom = %d, %v, fast=%t, bytes=%d", n, err, underlying.readFrom, obs.BytesAccepted())
	}
	registry.release(obs, OutcomeUnknown)
}

func TestObservedWriterHEADCountsZero(t *testing.T) {
	registry := NewRegistry(testConfig(), NewLocalStore(), nil)
	handler := registry.Observe(MediaRoute{Family: FamilyNative, Method: http.MethodHead, Pattern: "/head", Class: ClassPlayback, Role: RoleViewerEgress, Enrolled: true})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Attach(r.Context(), testAttachment("head"))
		_, _ = w.Write([]byte("not-on-wire"))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodHead, "/head", nil))
	if got := registry.Sweep().Sessions[0].BytesAccepted; got != 0 {
		t.Fatalf("HEAD bytes = %d", got)
	}
}

func TestObserveDoesNotRunGenericCaptureWhenRouteCaptureExists(t *testing.T) {
	originalNow := now
	defer func() { now = originalNow }()
	nowCalls := 0
	now = func() time.Time {
		nowCalls++
		return time.Unix(123, 0)
	}
	captureCalls := 0
	route := testRoute(ClassPlayback)
	route.Capture = func(*http.Request) CaptureSet {
		captureCalls++
		return CaptureSet{Method: http.MethodGet, Pattern: route.Pattern, ReceivedAt: time.Unix(100, 0)}
	}
	registry := NewRegistry(testConfig(), NewLocalStore(), nil)
	handler := registry.Observe(route)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/media/x", nil))
	if captureCalls != 1 || nowCalls != 0 {
		t.Fatalf("capture calls = %d, generic timestamp calls = %d", captureCalls, nowCalls)
	}
}

func TestObservedWriterPanicReleasesUnknownAndPropagates(t *testing.T) {
	registry := NewRegistry(testConfig(), NewLocalStore(), nil)
	handler := registry.Observe(testRoute(ClassPlayback))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		Attach(r.Context(), testAttachment("panic"))
		panic("boom")
	}))
	defer func() {
		if recover() == nil {
			t.Fatal("panic did not propagate")
		}
		snapshot := registry.Sweep()
		if snapshot.Sessions[0].Outcomes[OutcomeUnknown] != 1 {
			t.Fatalf("outcomes = %+v", snapshot.Sessions[0].Outcomes)
		}
	}()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/media/x", nil))
}

type optionalWriter struct{ *httptest.ResponseRecorder }

func (w *optionalWriter) Flush()                                       {}
func (w *optionalWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) { return nil, nil, nil }
func (w *optionalWriter) Push(string, *http.PushOptions) error         { return nil }

func TestObservedWriterPreservesOptionalInterfaces(t *testing.T) {
	obs := newObservation(nil, MediaRoute{}, CaptureSet{})
	w := &observedWriter{w: &optionalWriter{httptest.NewRecorder()}, observation: obs, bodyEligible: true}
	if _, _, err := w.Hijack(); err != nil {
		t.Fatal(err)
	}
	if err := w.Push("/asset", nil); err != nil {
		t.Fatal(err)
	}
	w.Flush()
}

type failingResponseWriter struct{ err error }

func (w *failingResponseWriter) Header() http.Header       { return make(http.Header) }
func (w *failingResponseWriter) WriteHeader(int)           {}
func (w *failingResponseWriter) Write([]byte) (int, error) { return 0, w.err }

type timeoutWriteError struct{}

func (timeoutWriteError) Error() string   { return "write deadline exceeded" }
func (timeoutWriteError) Timeout() bool   { return true }
func (timeoutWriteError) Temporary() bool { return false }

func TestObservedWriterClassifiesTransportFailuresOnRelease(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want httpstream.StreamOutcome
	}{
		{name: "stalled reap", err: timeoutWriteError{}, want: httpstream.OutcomeStalledReap},
		{name: "client gone", err: io.ErrClosedPipe, want: httpstream.OutcomeClientGone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry(testConfig(), NewLocalStore(), nil)
			obs := registry.begin(testRoute(ClassPlayback), CaptureSet{Method: http.MethodGet, Pattern: "/media/{id}", ReceivedAt: time.Now()})
			registry.attach(obs, testAttachment("failure"))
			writer := &observedWriter{w: &failingResponseWriter{err: test.err}, observation: obs, bodyEligible: true}
			_, _ = writer.Write([]byte("body"))
			registry.release(obs, obs.outcome(nil, true))
			session := registry.Sweep().Sessions[0]
			if session.Outcomes[test.want] != 1 {
				t.Fatalf("outcomes = %+v", session.Outcomes)
			}
		})
	}
}

// A route in an unobserved family must get the handler back unchanged — not a
// wrapper that decides per request — so the gate costs nothing on the hot path
// and cannot half-observe.
func TestObserveSkipsUnobservedFamily(t *testing.T) {
	cfg := testConfig()
	cfg.Families = map[Family]bool{FamilyNative: true}
	registry := NewRegistry(cfg, NewLocalStore(), nil)
	t.Cleanup(func() { _ = registry.Stop(context.Background()) })

	body := []byte("audiobook-bytes")
	serve := func(family Family) Snapshot {
		route := MediaRoute{Family: family, Method: http.MethodGet, Pattern: "/gated",
			Class: ClassPlayback, Role: RoleViewerEgress, CapRelevant: true, Enrolled: true}
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Attach(r.Context(), Attachment{Subject: UserSubject(7), SessionID: "gated-" + string(family),
				StartedAt: time.Unix(100, 0), StartedAtSource: StartedAtSourceSession})
			_, _ = w.Write(body)
		})
		wrapped := registry.Observe(route)(inner)
		if family != FamilyNative {
			// An unobserved family must be handed back the very handler it passed in.
			if fmt.Sprintf("%p", wrapped) != fmt.Sprintf("%p", http.Handler(inner)) {
				t.Fatalf("%s was wrapped despite being outside the observed set", family)
			}
		}
		recorder := httptest.NewRecorder()
		wrapped.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/gated", nil))
		if recorder.Body.String() != string(body) {
			t.Fatalf("%s body = %q", family, recorder.Body.String())
		}
		return registry.Sweep()
	}

	if snapshot := serve(FamilyABS); len(snapshot.Sessions) != 0 {
		t.Fatalf("gated-out family produced sessions: %+v", snapshot.Sessions)
	}
	snapshot := serve(FamilyNative)
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].BytesAccepted != int64(len(body)) {
		t.Fatalf("observed family sessions = %+v", snapshot.Sessions)
	}
}

// The cut flag has to be sampled BETWEEN slices, not once at entry. Write checks
// it every ~32 KiB and the HTTP/2 writer has no ReaderFrom so it falls back to
// Write; sampling once at entry left an HTTP/1.1 sendfile of the same session
// pouring until the whole file drained. That made a kill switch behave
// differently by protocol, which is why this test asserts both at once rather
// than pinning h1 alone.
func TestObservedWriterReadFromCutBehavesTheSameOverH1AndH2(t *testing.T) {
	const slices = 8
	total := slices * httpstream.ReadFromChunkDefault
	cutAfter := 2 * httpstream.ReadFromChunkDefault
	// One slice of overshoot is the floor on the zero-copy path: the kernel
	// drives a sendfile already in flight to completion and never calls back
	// into Go. The HTTP/2 fallback stops far sooner, and is held to the same
	// bound rather than a tighter one so the assertion is genuinely shared.
	limit := cutAfter + httpstream.ReadFromChunkDefault

	for _, test := range []struct {
		name  string
		http2 bool
	}{{name: "h1"}, {name: "h2", http2: true}} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry(testConfig(), NewLocalStore(), nil)

			var copied int64
			var copyErr error
			done := make(chan struct{})
			handler := registry.Observe(testRoute(ClassPlayback))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				Attach(r.Context(), testAttachment("session-cut"))
				observation, _ := r.Context().Value(observationContextKey{}).(*Observation)
				readerFrom, ok := w.(io.ReaderFrom)
				if !ok {
					t.Error("observedWriter does not implement io.ReaderFrom")
					return
				}
				copied, copyErr = readerFrom.ReadFrom(&cuttingReader{
					remaining: total, cutAt: cutAfter, observation: observation,
				})
			}))

			server := httptest.NewUnstartedServer(handler)
			server.EnableHTTP2 = test.http2
			if test.http2 {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()

			response, err := server.Client().Get(server.URL)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			if want := "HTTP/2.0"; test.http2 != (response.Proto == want) {
				t.Fatalf("proto = %q, http2 = %v", response.Proto, test.http2)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			<-done

			if copyErr == nil {
				t.Fatal("cut stream completed; the cut was only sampled at entry")
			}
			if copied > limit {
				t.Fatalf("copied %d bytes after a cut at %d, want at most %d", copied, cutAfter, limit)
			}
		})
	}
}

// cuttingReader trips the observation's cut flag once the stream has delivered
// cutAt bytes, standing in for an operator ending a session mid-transfer.
type cuttingReader struct {
	remaining   int64
	delivered   int64
	cutAt       int64
	observation *Observation
}

func (r *cuttingReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	r.remaining -= n
	r.delivered += n
	if r.delivered >= r.cutAt && r.observation != nil {
		r.observation.cut.Store(true)
	}
	return int(n), nil
}

// A cut transfer is not a completed delivery, whatever the write path managed to
// record. The two protocol shapes reach the cut differently — HTTP/1.1 has a
// ReaderFrom and stops between sendfile slices, while the HTTP/2 writer has none
// and falls back to Write — and both entry guards return before a byte is
// attempted, so firstWriteErr can still be nil when the observation is released.
// Classifying on that alone reported a deliberately severed stream as a full
// delivery, which is the one thing an operator who just killed a session must
// never read on the row they killed.
func TestObservedWriterCutIsNeverReleasedAsCompleted(t *testing.T) {
	tests := []struct {
		name string
		// readerFrom mirrors HTTP/1.1, whose writer has one; its absence mirrors
		// HTTP/2, which drives the same cut through Write instead.
		readerFrom bool
		// cutAtEntry sets the flag before the first write, so nothing is ever
		// attempted and no write error can exist to classify on.
		cutAtEntry bool
	}{
		{name: "h1 cut mid transfer", readerFrom: true},
		{name: "h2 cut mid transfer"},
		{name: "cut before the first write", readerFrom: true, cutAtEntry: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry(testConfig(), NewLocalStore(), nil)
			obs := registry.begin(testRoute(ClassPlayback), CaptureSet{Method: http.MethodGet, Pattern: "/media/{id}", ReceivedAt: time.Now()})
			registry.attach(obs, testAttachment("session-cut"))

			var inner http.ResponseWriter = httptest.NewRecorder()
			if test.readerFrom {
				inner = &readerFromRecorder{ResponseRecorder: httptest.NewRecorder()}
			}
			writer := &observedWriter{w: inner, observation: obs, bodyEligible: true}
			if test.cutAtEntry {
				obs.cut.Store(true)
			}
			// A cut on the first byte read keeps the transfer to one slice; the
			// point is which outcome is recorded, not how much escaped first.
			_, copyErr := writer.ReadFrom(&cuttingReader{
				remaining: 4 * httpstream.ReadFromChunkDefault, cutAt: 1, observation: obs,
			})
			if copyErr == nil {
				t.Fatal("cut transfer returned no error")
			}

			registry.release(obs, obs.outcome(nil, true))
			session := registry.Sweep().Sessions[0]
			if session.Outcomes[httpstream.OutcomeCompleted] != 0 {
				t.Fatalf("a cut transfer was released as completed: %+v", session.Outcomes)
			}
			if session.Outcomes[httpstream.OutcomeClientGone] != 1 {
				t.Fatalf("outcomes = %+v, want one %s", session.Outcomes, httpstream.OutcomeClientGone)
			}
		})
	}
}
