package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
)

// The mounted handlers use one independent admission, including compatibility
// without a user-data event hub. The startup pointer wiring is source-verified.
func TestClientSocketMountedSurfacesShareAccountAndProcessCapacity(t *testing.T) {
	admission, err := evt.NewClientSocketAdmission(1, 2, evt.ClientSocketEnforce)
	if err != nil {
		t.Fatal(err)
	}
	native := handlers.NewEventsHandler(evt.NewHub("fixture", nil), nil, nil, nil, nil, nil, nil)
	native.SetClientSocketAdmission(admission, t.Context(), nil)
	tickets := evt.NewSocketTicketStore(nil)
	v2 := &handlers.EventsSocketV2{Events: native, Tickets: tickets, Validate: func(ctx context.Context, id evt.SocketIdentity) (context.Context, *auth.Claims, error) {
		return ctx, &auth.Claims{UserID: id.UserID, Role: "user"}, nil
	}}
	sessions := jellycompat.NewSessionStore(time.Hour, nil)
	if err := sessions.Put(jellycompat.Session{Token: "fixture", StreamAppUserID: 7}); err != nil {
		t.Fatal(err)
	}
	router := http.NewServeMux()
	router.HandleFunc("/v1", native.HandleWebSocket)
	router.HandleFunc("/v2", v2.ServeHTTP)
	router.Handle("/", jellycompat.NewRouter(jellycompat.Dependencies{Config: &config.Config{}, SessionStore: sessions, ClientSocketAdmission: admission}))
	for _, processFull := range []bool{false, true} {
		held, refusal := admission.Reserve(7, false)
		if refusal != "" {
			t.Fatal(refusal)
		}
		var other *evt.ClientSocketReservation
		want := 429
		if processFull {
			other, refusal = admission.Reserve(8, false)
			if refusal != "" {
				t.Fatal(refusal)
			}
			want = 503
		}
		for _, path := range []string{"/v1", "/v2", "/Emby/Socket"} {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Sec-WebSocket-Version", "13")
			r.Header.Set("Sec-WebSocket-Key", "MDEyMzQ1Njc4OWFiY2RlZg==")
			r = r.WithContext(apimw.SetClaims(r.Context(), &auth.Claims{UserID: 7, Role: "user"}))
			r.Header.Set("X-Emby-Token", "fixture")
			var ticket string
			if path == "/v2" {
				ticket, err = tickets.Mint(t.Context(), evt.SocketIdentity{UserID: 7, SessionID: "fixture", AccessFingerprint: "scope", AccessExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Sec-WebSocket-Protocol", handlers.EventsSocketProtocol+", silo.ticket."+ticket)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != want || w.Header().Get("Retry-After") != "5" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s status=%d headers=%v body=%s", path, w.Code, w.Header(), w.Body.String())
			}
			if ticket != "" {
				if _, err := tickets.Consume(t.Context(), ticket); err == nil {
					t.Fatal("quota-refused ticket reusable")
				}
			}
		}
		held.Release()
		if other != nil {
			other.Release()
		}
		if admission.Occupancy() != 0 {
			t.Fatal("mounted refusal leaked reservation")
		}
	}
}
