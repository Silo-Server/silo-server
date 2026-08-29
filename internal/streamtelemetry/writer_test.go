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
