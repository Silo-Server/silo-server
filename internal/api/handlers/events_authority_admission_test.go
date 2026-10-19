package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/historyimport"

	"github.com/gorilla/websocket"
)

type authorityAdmissionSession struct{ revoked atomic.Bool }

func (s *authorityAdmissionSession) ActiveSession(context.Context, string) (auth.SessionAuthority, bool, error) {
	return auth.SessionAuthority{Role: "admin"}, !s.revoked.Load(), nil
}

type authorityAdmissionHistory struct {
	blocked  chan struct{}
	returned chan struct{}
	block    bool
}

func (h *authorityAdmissionHistory) ListAdminActiveRuns(ctx context.Context, _ *int) ([]historyimport.Run, error) {
	if h.block {
		close(h.blocked)
		<-ctx.Done()
		close(h.returned)
		// Even a dependency returning success after cancellation must not send
		// its stale result under authority the watcher has just revoked.
	}
	return nil, nil
}

func (h *authorityAdmissionHistory) ListActiveRuns(ctx context.Context, _ int) ([]historyimport.Run, error) {
	return h.ListAdminActiveRuns(ctx, nil)
}

type authorityAdmissionSocket struct {
	admissionEventSocket
	blocked       chan struct{}
	blockSnapshot bool
	snapshots     atomic.Int32
}

func (s *authorityAdmissionSocket) WriteJSON(value any) error {
	if _, snapshot := value.(evt.EventsSnapshotMessage); snapshot {
		s.snapshots.Add(1)
		if s.blockSnapshot {
			close(s.blocked)
			<-s.closed
			return io.ErrClosedPipe
		}
	}
	return s.admissionEventSocket.WriteJSON(value)
}

// Fake time and transports exercise the actual v1/v2 watcher paths while one
// query or write is blocked. No listener, real socket or store is involved.
func TestClientSocketAuthorityCancellationReleasesBlockedWork(t *testing.T) {
	for _, version := range []string{"v1", "v2 revoked", "v2 access changed"} {
		for _, work := range []string{"query", "write"} {
			t.Run(version+"/"+work, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					app, cancelApp := context.WithCancel(t.Context())
					defer cancelApp()
					a := testSocketAdmission(t)
					blocked := make(chan struct{})
					history := &authorityAdmissionHistory{blocked: blocked, returned: make(chan struct{}), block: work == "query"}
					socket := &authorityAdmissionSocket{
						admissionEventSocket: admissionEventSocket{closed: make(chan struct{}), writes: make(chan any, 8)},
						blocked:              blocked, blockSnapshot: work == "write",
					}
					h := &EventsHandler{hub: evt.NewHub("fixture", nil), socketAdmission: a, appContext: app, historyImports: history}
					h.upgradeSocket = func(http.ResponseWriter, *http.Request, websocket.Upgrader) (eventSocket, error) { return socket, nil }
					unsubscribed := false
					h.subscribeSocket = func() (<-chan evt.Envelope, func()) {
						return make(chan evt.Envelope), func() {
							select {
							case <-socket.closed:
							default:
								t.Error("unsubscribe preceded transport close")
							}
							if a.Occupancy() != 1 {
								t.Error("reservation released while subscribed")
							}
							unsubscribed = true
						}
					}
					r := eventAdmissionRequest(t, 7)
					r.URL.RawQuery = "channels=history_import"
					var handler http.Handler = http.HandlerFunc(h.HandleWebSocket)
					var revoke func()
					if version == "v1" {
						sessions := new(authorityAdmissionSession)
						h.sessionRoles, h.sessionCheckInterval = sessions, 10*time.Millisecond
						ctx := apimw.SetClaims(r.Context(), &auth.Claims{UserID: 7, Role: "admin", SessionID: "fixture-session"})
						ctx = apimw.SetActingAdminDecider(ctx, func(context.Context, apimw.ActingAdminRequest) (bool, error) { return true, nil })
						r = r.WithContext(ctx)
						revoke = func() { sessions.revoked.Store(true) }
					} else {
						v2, validity := socketTestHandler(t)
						v2.Events = h
						ticket, err := v2.Tickets.Mint(t.Context(), evt.SocketIdentity{UserID: 7, Role: "admin", SessionID: "fixture-session", AccessFingerprint: "scope", AccessExpiresAt: time.Now().Add(time.Minute)})
						if err != nil {
							t.Fatal(err)
						}
						r.Header.Set("Sec-WebSocket-Protocol", EventsSocketProtocol+", "+eventsTicketProtocolPrefix+ticket)
						handler = v2
						revoke = func() {
							err := evt.ErrSocketTicket
							if version == "v2 access changed" {
								err = errSocketAccessChanged
							}
							validity.fail(err)
						}
					}
					finished := make(chan struct{})
					go func() { defer close(finished); handler.ServeHTTP(httptest.NewRecorder(), r) }()
					<-blocked
					revoke()
					// Advance only the fake clock through the periodic check and
					// the bounded access_changed grace when the writer is blocked.
					time.Sleep(10 * time.Millisecond)
					synctest.Wait()
					if version == "v2 access changed" {
						select {
						case <-socket.closed:
							t.Error("access_changed lost its write grace")
						default:
						}
						time.Sleep(eventsAccessChangedGrace)
						synctest.Wait()
					}
					select {
					case <-finished:
					default:
						t.Error("authority cancellation retained blocked connection work")
						cancelApp()
						synctest.Wait()
						<-finished
					}
					if !unsubscribed || a.Occupancy() != 0 {
						t.Errorf("teardown incomplete: unsubscribed=%v occupancy=%d", unsubscribed, a.Occupancy())
					}
					if work == "query" && socket.snapshots.Load() != 0 {
						t.Error("canceled query emitted stale snapshot")
					}
				})
			})
		}
	}
}
