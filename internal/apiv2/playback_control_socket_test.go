package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/golang-jwt/jwt/v5"
)

type fakeControlSocket struct {
	available    bool
	calls        int
	identity     evt.SocketIdentity
	session      string
	installation string
	err          error
	served       int
}

func (f *fakeControlSocket) Available() bool { return f.available }
func (f *fakeControlSocket) Mint(_ context.Context, identity evt.SocketIdentity, session, installation string) (string, time.Time, error) {
	f.calls++
	f.identity, f.session, f.installation = identity, session, installation
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	return strings.Repeat("b", 43), time.Now().Add(30 * time.Second), nil
}
func (f *fakeControlSocket) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.served++
	http.Error(w, "invalid realtime credential", http.StatusUnauthorized)
}

const controlSocketSession = "3fa85f64-5717-4562-b3fc-2c963f66afa6"

func controlSocketDeps(f *fakeControlSocket) Dependencies {
	deps := pilotDeps(nil, nil)
	deps.PlaybackControlSocket = f
	claims := &auth.Claims{UserID: 1, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{map[string]*auth.Claims{memberToken: claims, "tok-unbounded": {UserID: 1, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess}}}, fakeSessions{map[string]bool{"s1": true}}, nil, nil)
	return deps
}

func TestPlaybackControlSocketTicketDelegatesOwnerAuthority(t *testing.T) {
	f := &fakeControlSocket{available: true}
	h := NewHandler(controlSocketDeps(f))
	path := Prefix + "/playback/sessions/" + controlSocketSession + "/control/ws-ticket"
	body := `{"installation_id":"` + playbackTestInstallation + `"}`

	rec := do(t, h, http.MethodPost, path, body, profileOwner())
	if rec.Code != 200 || f.calls != 1 || f.identity.UserID != 1 || f.identity.SessionID != "s1" || f.identity.ProfileID != "p-owner" || f.session != controlSocketSession || f.installation != playbackTestInstallation {
		t.Fatalf("delegation: %d %s %+v", rec.Code, rec.Body.String(), f)
	}
	if !strings.Contains(rec.Body.String(), `"protocol":"silo.playback-control.v2"`) || !strings.Contains(rec.Body.String(), `"max_connection_seconds":14400`) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("ticket body/headers: %s %v", rec.Body.String(), rec.Header())
	}

	// A bridge-started session mints without an installation.
	rec = do(t, h, http.MethodPost, path, `{}`, profileOwner())
	if rec.Code != 200 || f.installation != "" {
		t.Fatal(rec.Code, rec.Body.String(), f.installation)
	}

	// Profile scope is required; an account-only bearer cannot delegate.
	before := f.calls
	requireProblem(t, do(t, h, http.MethodPost, path, body, bearer(memberToken)), TypeValidationFailed)
	// A login without a bounded expiry cannot delegate.
	requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer("tok-unbounded"), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	// A malformed installation is refused before the seam.
	rec = do(t, h, http.MethodPost, path, `{"installation_id":"nope"}`, profileOwner())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if f.calls != before {
		t.Fatal("refused request reached the seam")
	}

	for _, mapping := range []struct {
		err  error
		want ProblemType
	}{
		{handlers.ErrPlaybackControlSocketNotOwner, TypePermissionDenied},
		{playback.ErrSessionNotFound, TypeNotFound},
		{handlers.ErrPlaybackControlSocketStale, TypeConflict},
		{handlers.ErrPlaybackControlSocketInstallation, TypeConflict},
		{handlers.ErrPlaybackControlSocketLaneHeld, TypeConflict},
		{evt.ErrSocketTicket, TypePermissionDenied},
		{handlers.ErrPlaybackControlSocketUnavailable, TypeDependencyUnavailable},
	} {
		f.err = mapping.err
		requireProblem(t, do(t, h, http.MethodPost, path, body, profileOwner()), mapping.want)
	}
}

func TestPlaybackControlSocketCapabilityAndRawRoute(t *testing.T) {
	capability := Prefix + "/playback/sessions/control/capabilities"
	socket := Prefix + "/playback/sessions/" + controlSocketSession + "/control/ws"

	deps := pilotDeps(nil, nil)
	h := NewHandler(deps)
	rec := do(t, h, http.MethodGet, capability, "", profileOwner())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":false`) || strings.Contains(rec.Body.String(), "owner_lease") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// Absent service: the ticket operation answers 503 and the raw route
	// refuses the handshake without a 404.
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/playback/sessions/"+controlSocketSession+"/control/ws-ticket", `{}`, profileOwner()), TypeDependencyUnavailable)
	rec = do(t, h, http.MethodGet, socket, "", nil)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(rec.Code, rec.Body.String())
	}

	f := &fakeControlSocket{available: true}
	h = NewHandler(controlSocketDeps(f))
	rec = do(t, h, http.MethodGet, capability, "", profileOwner())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":true`) || !strings.Contains(rec.Body.String(), `"protocol":"silo.playback-control.v2"`) || strings.Contains(rec.Body.String(), "owner_lease") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// The raw route delegates to the service without JSON negotiation.
	rec = do(t, h, http.MethodGet, socket, "", nil)
	if rec.Code != http.StatusUnauthorized || f.served != 1 {
		t.Fatal(rec.Code, rec.Body.String(), f.served)
	}
}
