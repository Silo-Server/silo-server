package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/realtimeadmission"
)

type realtimeAdmissionService struct {
	handlers.EventsHandler
	refusal error
}

func (s *realtimeAdmissionService) Mint(context.Context, evt.SocketIdentity) (string, error) {
	return "synthetic-ticket", s.refusal
}
func (s *realtimeAdmissionService) MintRoomSocket(context.Context, string, string, evt.SocketIdentity) (string, time.Time, error) {
	return "synthetic-ticket", time.Now().Add(time.Second), s.refusal
}
func (*realtimeAdmissionService) ServeHTTP(http.ResponseWriter, *http.Request) {}
func (*realtimeAdmissionService) RealtimeWorkStatus() *realtimeadmission.Status {
	return &realtimeadmission.Status{Version: 1, Backend: "memory", Scope: "per_node", Epoch: "synthetic-epoch"}
}

func TestRealtimeTicketAdmissionProblemsAndRecovery(t *testing.T) {
	for _, path := range []string{Prefix + "/events/ws-ticket", Prefix + "/watch-together/rooms/room/ws-ticket"} {
		t.Run(path, func(t *testing.T) {
			deps := pilotDeps(nil, nil)
			service := new(realtimeAdmissionService)
			deps.EventsSocket = service
			deps.WatchTogetherSocket = service
			claims := &auth.Claims{UserID: 1, Role: "user", SessionID: "session", TokenType: auth.TokenTypeAccess, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
			deps.Auth = apimw.NewAuthMiddleware(fakeTokens{map[string]*auth.Claims{memberToken: claims}}, fakeSessions{map[string]string{"session": "user"}}, nil, nil)
			handler := NewHandler(deps)
			headers := profileOwner()
			headers["X-Room-Token"] = "synthetic-proof"
			for _, tc := range []struct {
				code   string
				status int
				retry  time.Duration
				header string
			}{
				{"realtime_rate_limited", 429, 1250 * time.Millisecond, "2"}, {"realtime_unavailable", 503, 2 * time.Second, "2"},
			} {
				service.refusal = &realtimeadmission.Refusal{Code: tc.code, Message: "Synthetic refusal.", RetryAfter: tc.retry}
				rec := do(t, handler, http.MethodPost, path, "", headers)
				var problem Problem
				if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
					t.Fatal(err)
				}
				if rec.Code != tc.status || problem.Code != tc.code || problem.WorkStarted == nil || *problem.WorkStarted || problem.RetryAfterMS != tc.retry.Milliseconds() || rec.Header().Get("Retry-After") != tc.header {
					t.Fatalf("refusal mapping: %d %s %v", rec.Code, rec.Body.String(), rec.Header())
				}
				if rec.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("cacheable refusal")
				}
			}
			service.refusal = nil
			rec := do(t, handler, http.MethodPost, path, "", headers)
			var ticket EventsSocketTicket
			if err := json.Unmarshal(rec.Body.Bytes(), &ticket); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 200 || ticket.WorkAdmission == nil || ticket.WorkAdmission.Version != 1 || ticket.WorkAdmission.Scope != "per_node" || ticket.WorkAdmission.Epoch != "synthetic-epoch" {
				t.Fatalf("recovery status: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRealtimeCapabilitiesExposeStartupWorkStatus(t *testing.T) {
	deps := pilotDeps(nil, nil)
	service := new(realtimeAdmissionService)
	deps.EventsCapability = service
	deps.WatchTogetherCapability = &fakeWatchTogetherCapability{available: true}
	deps.WatchTogetherSocket = service
	handler := NewHandler(deps)
	for _, path := range []string{Prefix + "/events/capabilities", Prefix + "/watch-together/capabilities"} {
		rec := do(t, handler, http.MethodGet, path, "", bearer(memberToken))
		var body struct {
			WorkAdmission *realtimeadmission.Status `json:"work_admission"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 200 || body.WorkAdmission == nil || body.WorkAdmission.Backend != "memory" || body.WorkAdmission.Scope != "per_node" {
			t.Fatalf("capability status: %d %s", rec.Code, rec.Body.String())
		}
	}
}
