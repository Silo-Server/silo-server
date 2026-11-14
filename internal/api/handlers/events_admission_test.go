package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func testSocketAdmission(t *testing.T) *evt.ClientSocketAdmission {
	t.Helper()
	a, err := evt.NewClientSocketAdmission(2, 3, evt.ClientSocketEnforce)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func eventAdmissionRequest(t *testing.T, userID int) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/events/ws", nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Key", "MDEyMzQ1Njc4OWFiY2RlZg==")
	if userID != 0 {
		r = r.WithContext(apimw.SetClaims(r.Context(), &auth.Claims{UserID: userID, Role: "user"}))
	}
	return r
}

func TestClientSocketV2QuotaConsumesTicketWithoutUpgrade(t *testing.T) {
	h, _ := socketTestHandler(t)
	a := h.Events.socketAdmission
	first, _ := a.Reserve(7, false)
	second, _ := a.Reserve(7, false)
	defer first.Release()
	defer second.Release()
	upgrades := 0
	h.Events.upgradeSocket = func(http.ResponseWriter, *http.Request, websocket.Upgrader) (eventSocket, error) {
		upgrades++
		return nil, errors.New("unexpected")
	}
	ticket := socketTestTicket(t, h, time.Now().Add(time.Minute))
	r := eventAdmissionRequest(t, 0)
	r.Header.Set("Sec-WebSocket-Protocol", EventsSocketProtocol+", "+eventsTicketProtocolPrefix+ticket)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 429 || upgrades != 0 || a.Occupancy() != 2 {
		t.Fatalf("status=%d upgrades=%d count=%d", w.Code, upgrades, a.Occupancy())
	}
	if _, err := h.Tickets.Consume(t.Context(), ticket); err == nil {
		t.Fatal("quota-refused v2 ticket reusable")
	}
}

// serveEventsV2Fixture drives h through the v2 ticket handshake for user 7.
func serveEventsV2Fixture(t *testing.T, h *EventsHandler, w http.ResponseWriter, query string) {
	t.Helper()
	v2, _ := socketTestHandler(t)
	v2.Events = h
	ticket := socketTestTicket(t, v2, time.Now().Add(time.Minute))
	r := eventAdmissionRequest(t, 0)
	r.URL.RawQuery = query
	r.Header.Set("Sec-WebSocket-Protocol", EventsSocketProtocol+", "+eventsTicketProtocolPrefix+ticket)
	v2.ServeHTTP(w, r)
}

type admissionEventSocket struct {
	closed chan struct{}
	writes chan any
	once   sync.Once
}

func (s *admissionEventSocket) Close() error                            { s.once.Do(func() { close(s.closed) }); return nil }
func (s *admissionEventSocket) ReadMessage() (int, []byte, error)       { <-s.closed; return 0, nil, io.EOF }
func (s *admissionEventSocket) WriteJSON(v any) error                   { s.writes <- v; return nil }
func (*admissionEventSocket) WriteControl(int, []byte, time.Time) error { return nil }
func (*admissionEventSocket) SetWriteDeadline(time.Time) error          { return nil }
func (*admissionEventSocket) SetReadDeadline(time.Time) error           { return nil }
func (*admissionEventSocket) SetReadLimit(int64)                        {}
func (*admissionEventSocket) SetPongHandler(func(string) error)         {}

func TestClientSocketAppCancellationClosesAndUnsubscribesBeforeRelease(t *testing.T) {
	a := testSocketAdmission(t)
	app, cancel := context.WithCancel(t.Context())
	defer cancel()
	socket := &admissionEventSocket{closed: make(chan struct{}), writes: make(chan any, 8)}
	unsubscribed := make(chan struct{})
	h := &EventsHandler{hub: evt.NewHub("fixture", nil), socketAdmission: a, appContext: app}
	h.upgradeSocket = func(http.ResponseWriter, *http.Request, websocket.Upgrader) (eventSocket, error) { return socket, nil }
	h.subscribeSocket = func() (<-chan evt.Envelope, func()) {
		return make(chan evt.Envelope), func() {
			select {
			case <-socket.closed:
			default:
				t.Error("unsubscribe before connection close")
			}
			if a.Occupancy() != 1 {
				t.Error("capacity released before unsubscribe")
			}
			close(unsubscribed)
		}
	}
	finished := make(chan struct{})
	go func() { defer close(finished); serveEventsV2Fixture(t, h, httptest.NewRecorder(), "") }()
	select {
	case <-socket.writes:
	case <-time.After(time.Second):
		t.Fatal("no hello")
	}
	if a.Occupancy() != 1 {
		t.Fatal("active socket not counted")
	}
	a.BeginDrain()
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("socket did not finish on app cancellation")
	}
	select {
	case <-unsubscribed:
	default:
		t.Fatal("missing unsubscribe")
	}
	select {
	case <-a.Done():
	default:
		t.Fatal("shutdown still retains reservation")
	}
	if a.Occupancy() != 0 {
		t.Fatal("socket leaked reservation")
	}
}

func TestClientSocketFailedUpgradeDoesNotStartWatcher(t *testing.T) {
	h := &EventsHandler{hub: evt.NewHub("fixture", nil)}
	started := false
	h.upgradeSocket = func(http.ResponseWriter, *http.Request, websocket.Upgrader) (eventSocket, error) {
		return nil, errors.New("fixture")
	}
	h.serveWebSocket(httptest.NewRecorder(), eventAdmissionRequest(t, 7), &auth.Claims{UserID: 7}, "", websocket.Upgrader{}, nil, func(context.Context, context.CancelFunc) { started = true })
	if started {
		t.Fatal("failed upgrade started recurring watcher")
	}
}

// An initial write can fail before the reader is started. Even this path closes
// the upgraded transport before detaching its queue and releasing capacity.
func TestClientSocketHelloFailureClosesBeforeUnsubscribe(t *testing.T) {
	a := testSocketAdmission(t)
	socket := &failingAdmissionEventSocket{admissionEventSocket: admissionEventSocket{closed: make(chan struct{}), writes: make(chan any, 1)}}
	h := &EventsHandler{hub: evt.NewHub("fixture", nil), socketAdmission: a}
	h.upgradeSocket = func(http.ResponseWriter, *http.Request, websocket.Upgrader) (eventSocket, error) { return socket, nil }
	unsubscribed := false
	h.subscribeSocket = func() (<-chan evt.Envelope, func()) {
		return make(chan evt.Envelope), func() {
			select {
			case <-socket.closed:
			default:
				t.Error("queue detached before close")
			}
			if a.Occupancy() != 1 {
				t.Error("released while subscribed")
			}
			unsubscribed = true
		}
	}
	serveEventsV2Fixture(t, h, httptest.NewRecorder(), "")
	if !unsubscribed || a.Occupancy() != 0 {
		t.Fatal("hello failure leaked")
	}
}

type failingAdmissionEventSocket struct{ admissionEventSocket }

func (*failingAdmissionEventSocket) WriteJSON(any) error { return io.ErrClosedPipe }

func TestClientSocketV2NilAndDrainDoNotConsumeTicket(t *testing.T) {
	for _, absent := range []bool{false, true} {
		h, _ := socketTestHandler(t)
		admission := h.Events.socketAdmission
		ticket := socketTestTicket(t, h, time.Now().Add(time.Minute))
		if absent {
			h.Events.socketAdmission = nil
		} else {
			h.Events.shuttingDown = func() bool { return true }
		}
		r := eventAdmissionRequest(t, 0)
		r.Header.Set("Sec-WebSocket-Protocol", EventsSocketProtocol+", "+eventsTicketProtocolPrefix+ticket)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 503 {
			t.Fatalf("status=%d", w.Code)
		}
		if _, err := h.Tickets.Consume(t.Context(), ticket); err != nil {
			t.Fatal("pre-admission availability check spent ticket")
		}
		count := "1"
		if absent {
			count = "0" // Missing dependencies cannot invent a collector.
		}
		want := `
# HELP silo_events_client_socket_refusals_total Refused client socket reservations by bounded reason.
# TYPE silo_events_client_socket_refusals_total counter
silo_events_client_socket_refusals_total{reason="account"} 0
silo_events_client_socket_refusals_total{reason="process"} 0
silo_events_client_socket_refusals_total{reason="draining"} ` + count + `
silo_events_client_socket_refusals_total{reason="unavailable"} 0
`
		if err := testutil.CollectAndCompare(admission, strings.NewReader(want), "silo_events_client_socket_refusals_total"); err != nil {
			t.Fatal(err)
		}
		if admission.Occupancy() != 0 {
			t.Fatal("early refusal reserved capacity")
		}
	}
}

// A client that hangs up mid-handshake is not a shutdown: it must not answer
// 503 or count toward the draining refusals the rollout gates read.
func TestClientSocketAbandonedHandshakeIsNotDraining(t *testing.T) {
	h, _ := socketTestHandler(t)
	admission := h.Events.socketAdmission
	ticket := socketTestTicket(t, h, time.Now().Add(time.Minute))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := eventAdmissionRequest(t, 0).WithContext(ctx)
	r.Header.Set("Sec-WebSocket-Protocol", EventsSocketProtocol+", "+eventsTicketProtocolPrefix+ticket)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("abandoned handshake answered as draining")
	}
	want := `
# HELP silo_events_client_socket_refusals_total Refused client socket reservations by bounded reason.
# TYPE silo_events_client_socket_refusals_total counter
silo_events_client_socket_refusals_total{reason="account"} 0
silo_events_client_socket_refusals_total{reason="process"} 0
silo_events_client_socket_refusals_total{reason="draining"} 0
silo_events_client_socket_refusals_total{reason="unavailable"} 0
`
	if err := testutil.CollectAndCompare(admission, strings.NewReader(want), "silo_events_client_socket_refusals_total"); err != nil {
		t.Fatal(err)
	}
	if admission.Occupancy() != 0 {
		t.Fatal("abandoned handshake leaked its reservation")
	}
}
