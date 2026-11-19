package streamtelemetry

import (
	"bufio"
	"errors"
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

type optionalWriter struct {
	*httptest.ResponseRecorder
	hijackErr   error
	pushErr     error
	pushTarget  string
	pushOptions *http.PushOptions
}

func (w *optionalWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, w.hijackErr
}
func (w *optionalWriter) Push(target string, options *http.PushOptions) error {
	w.pushTarget, w.pushOptions = target, options
	return w.pushErr
}

func TestObservedWriterPreservesOptionalInterfaces(t *testing.T) {
	obs := newObservation(nil, MediaRoute{}, CaptureSet{})
	underlying := &optionalWriter{
		ResponseRecorder: httptest.NewRecorder(),
		hijackErr:        errors.New("hijack failed"),
		pushErr:          errors.New("push failed"),
	}
	w := &observedWriter{w: underlying, observation: obs, bodyEligible: true}
	if _, _, err := w.Hijack(); !errors.Is(err, underlying.hijackErr) {
		t.Fatalf("Hijack error = %v, want underlying error", err)
	}
	options := &http.PushOptions{Method: http.MethodGet}
	if err := w.Push("/asset", options); !errors.Is(err, underlying.pushErr) {
		t.Fatalf("Push error = %v, want underlying error", err)
	}
	if underlying.pushTarget != "/asset" || underlying.pushOptions != options {
		t.Fatalf("Push forwarded %q, %v", underlying.pushTarget, underlying.pushOptions)
	}
	w.Flush()
	if !underlying.Flushed {
		t.Fatal("Flush did not reach the underlying writer")
	}
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
