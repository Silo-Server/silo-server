package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	visitorSession = "visitor-session"
	visitorRoom    = "room-1"
	visitorID      = "visitor-1"
	hostProfile    = "host-profile"
)

var visitorAuthority = auth.SessionAuthority{Role: models.RoleUser, Purpose: models.SessionPurposeWatchPartyVisitor,
	BoundProfileID: hostProfile, RoomID: visitorRoom, VisitorID: visitorID}

func visitorSessions() *fakeSessionValidator {
	return &fakeSessionValidator{visitors: map[string]auth.SessionAuthority{visitorSession: visitorAuthority}}
}

// visitorRequest runs one request through RequireAuth on a visitor session
// of an admin host account.
func visitorRequest(t *testing.T, method, target, profileID string) (*httptest.ResponseRecorder, *auth.Claims, string) {
	t.Helper()
	claims := auth.Claims{UserID: 42, Role: models.RoleAdmin, SessionID: visitorSession, TokenType: auth.TokenTypeAccess}
	am := NewAuthMiddleware(claimsValidator{claims}, visitorSessions(), nil, nil)
	var seen *auth.Claims
	var seenProfile string
	h := am.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = GetClaims(r.Context())
		seenProfile = r.Header.Get("X-Profile-Id")
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Authorization", "Bearer token")
	if profileID != "" {
		req.Header.Set("X-Profile-Id", profileID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, seen, seenProfile
}

// A visitor session reaches its own room and the room's playback, as the
// host's profile with the user role, and nothing else.
func TestRequireAuthConfinesWatchPartyVisitor(t *testing.T) {
	room := "/api/v2/watch-together/rooms/" + visitorRoom
	other := "/api/v2/watch-together/rooms/room-2"
	for _, tc := range []struct {
		name, method, target, profile string
		allowed                       bool
	}{
		{"read its room", http.MethodGet, room, "", true},
		{"open its room socket", http.MethodPost, room + "/ws-ticket", hostProfile, true},
		{"suggest in its room", http.MethodPost, room + "/suggestions", "", true},
		{"vote in its room", http.MethodPost, room + "/suggestions/s1/vote", "", true},
		{"report a refused source", http.MethodPost, room + "/source-fallback", "", true},
		{"start playback", http.MethodPost, "/api/v2/playback/start", "", true},
		{"stream", http.MethodGet, "/api/v2/stream/abc", "", true},
		{"sign out", http.MethodPost, "/api/v2/auth/logout", "", true},
		{"read another room", http.MethodGet, other, "", false},
		{"socket for another room", http.MethodPost, other + "/ws-ticket", "", false},
		{"read member watch state", http.MethodPost, room + "/member-state", "", false},
		{"pick for the room", http.MethodPut, room + "/selection", "", false},
		{"start the room", http.MethodPost, room + "/playback/start", "", false},
		{"close the room", http.MethodDelete, room, "", false},
		{"promote a suggestion", http.MethodPost, room + "/suggestions/promote", "", false},
		{"invite more visitors", http.MethodPost, room + "/visitor-invites", "", false},
		{"remove a visitor", http.MethodDelete, room + "/visitors/visitor-2", "", false},
		{"create a room", http.MethodPost, "/api/v2/watch-together/rooms", "", false},
		{"join a room", http.MethodPost, "/api/v2/watch-together/join", "", false},
		{"browse the catalog", http.MethodGet, "/api/v2/catalog/items/x", "", false},
		{"read the account", http.MethodGet, "/api/v2/account/me", "", false},
		{"list profiles", http.MethodGet, "/api/v2/profiles", "", false},
		{"open the events socket", http.MethodPost, "/api/v2/events/ws-ticket", "", false},
		{"save progress", http.MethodPost, "/api/v2/sync/progress", "", false},
		{"v1", http.MethodGet, "/api/v1/items", "", false},
		{"unclean path into its room", http.MethodGet, other + "/../" + visitorRoom, "", false},
		{"act as another profile", http.MethodGet, room, "kid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, claims, profile := visitorRequest(t, tc.method, tc.target, tc.profile)
			if !tc.allowed {
				if rec.Code != http.StatusForbidden || claims != nil {
					t.Fatalf("status = %d, claims = %+v; want 403", rec.Code, claims)
				}
				return
			}
			if rec.Code != http.StatusNoContent || claims == nil {
				t.Fatalf("status = %d %s; want admitted", rec.Code, rec.Body.String())
			}
			if claims.Role != models.RoleUser || !claims.IsWatchPartyVisitor() || claims.IsOwnLoginSession() ||
				claims.WatchPartyRoomID != visitorRoom || claims.WatchPartyVisitorID != visitorID || profile != hostProfile {
				t.Fatalf("claims = %+v, profile = %q", claims, profile)
			}
		})
	}
}

// Public routes answer a visitor anonymously: it is not a signed-in person
// of the host's account.
func TestAttachSessionLeavesWatchPartyVisitorAnonymous(t *testing.T) {
	claims := auth.Claims{UserID: 42, Role: models.RoleAdmin, SessionID: visitorSession, TokenType: auth.TokenTypeAccess}
	am := NewAuthMiddleware(claimsValidator{claims}, visitorSessions(), nil, nil)
	var seen *auth.Claims
	h := am.AttachSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = GetClaims(r.Context()) }))
	req := httptest.NewRequest(http.MethodGet, "/api/v2/auth/device", nil)
	req.Header.Set("Authorization", "Bearer token")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != nil {
		t.Fatalf("attached claims = %+v; want anonymous", seen)
	}
}

// Download links are refused for a visitor session, and RevalidateCurrent
// holds it to its room.
func TestDerivedCredentialsRefuseWatchPartyVisitor(t *testing.T) {
	jwt := auth.NewJWTService("test-secret", 15*time.Minute, 7*24*time.Hour)
	am := NewAuthMiddleware(jwt, visitorSessions(), nil,
		userLoaderFunc(func(context.Context, int) (*models.User, error) { return &models.User{ID: 42, Enabled: true}, nil }))
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	link, _, err := jwt.GenerateDirectDownloadLinkToken(42, models.RoleUser, visitorSession, hostProfile, nil, 9)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v2/direct-download?file_id=9&"+DirectDownloadLinkParam+"="+link, nil)
	rec := httptest.NewRecorder()
	am.RequireDirectDownloadAuth(ok).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("download link: %d %s", rec.Code, rec.Body.String())
	}

	claims := auth.Claims{UserID: 42, Role: models.RoleUser, SessionID: visitorSession, TokenType: auth.TokenTypeAccess, ProfileID: hostProfile, SessionPurpose: models.SessionPurposeWatchPartyVisitor}
	cam := NewAuthMiddleware(claimsValidator{claims}, visitorSessions(), nil,
		userLoaderFunc(func(context.Context, int) (*models.User, error) { return &models.User{ID: 42, Enabled: true}, nil }))
	if err := cam.RevalidateCurrent(t.Context(), "Bearer token", &claims, http.MethodGet, "/api/v2/watch-together/rooms/"+visitorRoom); err != nil {
		t.Fatalf("own room: %v", err)
	}
	if err := cam.RevalidateCurrent(t.Context(), "Bearer token", &claims, http.MethodGet, "/api/v2/watch-together/rooms/room-2"); !errors.Is(err, ErrCurrentCredentialForbidden) {
		t.Fatalf("other room: %v", err)
	}
}

// The visitor route lists admit exactly the operations a visitor needs, and
// no other operation in the v2 contract. A new operation whose path happens
// to match a listed pattern fails here until it is reviewed.
func TestWatchPartyVisitorRoutesAgainstContract(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/api/v2/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"logout": false, "getWatchTogetherCapabilities": false, "getPlaybackCapabilities": false,
		"startPlayback": false, "reportPlaybackRouteEvent": false, "stopPlayback": false,
		"updatePlaybackProgress": false, "replanPlayback": false,
		"getWatchTogetherRoom": false, "fallbackWatchTogetherSource": false,
		"createWatchTogetherSocketTicket": false, "listWatchTogetherSuggestions": false,
		"createWatchTogetherSuggestion": false, "deleteWatchTogetherSuggestion": false,
		"voteWatchTogetherSuggestion": false, "unvoteWatchTogetherSuggestion": false,
	}
	for p, ops := range contract.Paths {
		concrete := strings.ReplaceAll(p, "{room_id}", visitorRoom)
		for strings.Contains(concrete, "{") {
			start := strings.Index(concrete, "{")
			end := strings.Index(concrete[start:], "}")
			concrete = concrete[:start] + "x1" + concrete[start+end+1:]
		}
		for method, op := range ops {
			if !watchPartyVisitorAllowsRoute(strings.ToUpper(method), concrete, visitorRoom) {
				continue
			}
			if _, ok := want[op.OperationID]; !ok {
				// Stream and transcode media routes are raw routes; any
				// other admitted operation is a review failure.
				if !strings.HasPrefix(p, "/api/v2/stream/") && !strings.HasPrefix(p, "/api/v2/playback/transcode/") {
					t.Errorf("visitor admits %s %s (%s)", strings.ToUpper(method), p, op.OperationID)
				}
				continue
			}
			want[op.OperationID] = true
		}
	}
	for op, admitted := range want {
		if !admitted {
			t.Errorf("visitor does not admit %s", op)
		}
	}
}
