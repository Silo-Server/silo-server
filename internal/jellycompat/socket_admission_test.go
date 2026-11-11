package jellycompat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	evt "github.com/Silo-Server/silo-server/internal/events"
)

func socketTestLifecycle(t testing.TB) SocketLifecycle {
	t.Helper()
	a, err := evt.NewClientSocketAdmission(2, 3, evt.ClientSocketEnforce)
	if err != nil {
		t.Fatal(err)
	}
	return SocketLifecycle{Admission: a}
}

type admissionNoticeSource struct {
	*fakeUserStateEvents
	subscribed int
}

func (s *admissionNoticeSource) Subscribe() (<-chan evt.Envelope, func()) {
	s.subscribed++
	return s.fakeUserStateEvents.Subscribe()
}

func TestClientSocketFailedUpgradeDoesNotSubscribe(t *testing.T) {
	sessions := NewSessionStore(time.Hour, nil)
	if err := sessions.Put(Session{Token: "fixture", StreamAppUserID: 3}); err != nil {
		t.Fatal(err)
	}
	source := &admissionNoticeSource{fakeUserStateEvents: newFakeUserStateEvents()}
	handler := NewSocketHandlerWithUserData(sessions, nil, source, NewResourceIDCodec(), socketTestLifecycle(t))
	req := httptest.NewRequest("GET", "/socket", nil)
	req.Header.Set("X-Emby-Token", "fixture")
	handler(httptest.NewRecorder(), req)
	if source.subscribed != 0 {
		t.Fatalf("failed upgrade allocated %d subscriptions; want none", source.subscribed)
	}
}

func TestClientSocketAliasesShareAdmissionWithoutNotices(t *testing.T) {
	lifecycle := socketTestLifecycle(t)
	first, _ := lifecycle.Admission.Reserve(3, false)
	second, _ := lifecycle.Admission.Reserve(3, false)
	defer first.Release()
	defer second.Release()
	sessions := NewSessionStore(time.Hour, nil)
	if err := sessions.Put(Session{Token: "fixture", StreamAppUserID: 3}); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Dependencies{Config: &config.Config{}, SessionStore: sessions, ClientSocketAdmission: lifecycle.Admission})
	for _, path := range []string{"/socket", "/Socket", "/SOCKET", "/emby/socket", "/jellyfin/socket", "/Emby/Socket", "/JELLYFIN/SOCKET"} {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Header.Set("X-Emby-Token", "fixture")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != 429 || w.Header().Get("Retry-After") != "5" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("alias bypass: status %d body %s", w.Code, w.Body.String())
			}
		})
	}
	first.Release()
	second.Release()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/socket", nil)
	r.Header.Set("X-Emby-Token", "fixture")
	router.ServeHTTP(w, r)
	if w.Code != 400 || lifecycle.Admission.Occupancy() != 0 {
		t.Fatalf("failed upgrade leak/status %d count %d", w.Code, lifecycle.Admission.Occupancy())
	}
}

type admissionCompatSocket struct {
	closed chan struct{}
	writes chan any
	once   sync.Once
}

func (s *admissionCompatSocket) Close() error                            { s.once.Do(func() { close(s.closed) }); return nil }
func (s *admissionCompatSocket) ReadJSON(any) error                      { <-s.closed; return io.EOF }
func (s *admissionCompatSocket) WriteJSON(v any) error                   { s.writes <- v; return nil }
func (*admissionCompatSocket) WriteControl(int, []byte, time.Time) error { return nil }
func (*admissionCompatSocket) SetWriteDeadline(time.Time) error          { return nil }
func (*admissionCompatSocket) SetReadLimit(int64)                        {}

func TestClientSocketKeepAliveOnlyDrainsAndReleases(t *testing.T) {
	lifecycle := socketTestLifecycle(t)
	app, cancel := context.WithCancel(t.Context())
	defer cancel()
	lifecycle.AppContext = app
	socket := &admissionCompatSocket{closed: make(chan struct{}), writes: make(chan any, 2)}
	lifecycle.upgrade = func(http.ResponseWriter, *http.Request) (compatSocket, error) {
		if lifecycle.Admission.Occupancy() != 1 {
			t.Error("upgrade not reserved")
		}
		return socket, nil
	}
	sessions := NewSessionStore(time.Hour, nil)
	if err := sessions.Put(Session{Token: "fixture", StreamAppUserID: 3}); err != nil {
		t.Fatal(err)
	}
	handler := NewSocketHandler(sessions, nil, lifecycle)
	r := httptest.NewRequest(http.MethodGet, "/socket", nil)
	r.Header.Set("X-Emby-Token", "fixture")
	done := make(chan struct{})
	go func() { defer close(done); handler(httptest.NewRecorder(), r) }()
	select {
	case v := <-socket.writes:
		if msg, ok := v.(wsMessage); !ok || msg.MessageType != "ForceKeepAlive" || string(msg.Data) != "60" {
			t.Fatalf("keepalive changed: %+v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("no ForceKeepAlive")
	}
	if lifecycle.Admission.Occupancy() != 1 {
		t.Fatal("keepalive-only socket not accounted")
	}
	lifecycle.Admission.BeginDrain()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("compat socket did not finish")
	}
	select {
	case <-socket.closed:
	default:
		t.Fatal("capacity returned with open connection")
	}
	select {
	case <-lifecycle.Admission.Done():
	default:
		t.Fatal("socket reservation leaked")
	}
}

func TestClientSocketCompatUnavailableAndCanceled(t *testing.T) {
	sessions := NewSessionStore(time.Hour, nil)
	if err := sessions.Put(Session{Token: "fixture", StreamAppUserID: 3}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	lifecycle := socketTestLifecycle(t)
	lifecycle.AppContext = ctx
	for _, l := range []SocketLifecycle{{}, lifecycle} {
		r := httptest.NewRequest(http.MethodGet, "/socket", nil)
		r.Header.Set("X-Emby-Token", "fixture")
		w := httptest.NewRecorder()
		NewSocketHandler(sessions, nil, l)(w, r)
		if w.Code != 503 || w.Header().Get("Retry-After") != "5" {
			t.Fatalf("nil/canceled lifecycle status %d", w.Code)
		}
	}
}
