package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminLoginSessions struct {
	userID    int
	sessionID *string
	writes    int
}

func (*fakeAdminLoginSessions) AdminLoginSessionsAvailable() bool { return true }
func (f *fakeAdminLoginSessions) ListAdminLoginSessions(_ context.Context, id int, _ *auth.SessionKey, _ int) ([]*models.AuthSession, bool, error) {
	f.userID = id
	return []*models.AuthSession{{ID: "00000000-0000-0000-0000-000000000001", DeviceName: "Test client", CreatedAt: fixedTime(), ExpiresAt: fixedTime().Add(30 * 24 * time.Hour)}}, true, nil
}
func (f *fakeAdminLoginSessions) RevokeAdminLoginSessions(_ context.Context, id int, sessionID *string) (int, error) {
	f.userID = id
	f.sessionID = sessionID
	f.writes++
	return 1, nil
}

func TestAdminLoginSessionsAuthorityAndCursorIsolation(t *testing.T) {
	f := new(fakeAdminLoginSessions)
	deps := pilotDeps(nil, nil)
	deps.AdminLoginSessions = f
	deps.ActingAdmin = apimw.RequireActingAdmin(func(context.Context, int, string) (bool, bool, error) { return true, true, nil }, nil)
	h := newTestHandler(t, deps)
	headers := with(bearer(adminToken), "X-Profile-Id", "p-primary")
	root := Prefix + "/admin/users/7/login-sessions"
	requireProblem(t, do(t, h, "GET", root, "", with(bearer(memberToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	rec := do(t, h, "GET", root+"?limit=1", "", headers)
	if rec.Code != 200 || f.userID != 7 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	page := decodeLoginSessions(t, rec.Body)
	cursor := url.QueryEscape(page.Page.NextCursor)
	requireProblem(t, do(t, h, "GET", Prefix+"/admin/users/8/login-sessions?limit=1&cursor="+cursor, "", headers), TypeInvalidCursor)
	requireProblem(t, do(t, h, "GET", root+"?limit=1&cursor="+cursor, "", with(bearer(otherAdminToken), "X-Profile-Id", "p-primary")), TypeInvalidCursor)
	requireProblem(t, do(t, h, "GET", Prefix+"/auth/sessions?cursor="+cursor, "", bearer(adminToken)), TypeInvalidCursor)
	rec = do(t, h, "DELETE", root+"/00000000-0000-0000-0000-000000000001", "", headers)
	if rec.Code != 204 || f.sessionID == nil || f.userID != 7 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "DELETE", root, "", headers)
	if rec.Code != 200 || f.sessionID != nil || f.writes != 2 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	requireProblem(t, do(t, h, "DELETE", root+"/malformed", "", headers), TypeValidationFailed)
}

func TestLoginSessionMetadataPinsCurrentOffPage(t *testing.T) {
	seen := fixedTime()
	current := &models.AuthSession{ID: "s1", UserID: 1, DeviceName: "This client", CreatedAt: seen, ExpiresAt: seen, LastSeenAt: &seen}
	deps := pilotDeps(nil, nil)
	deps.Sessions = &fakeSessionService{current: current}
	rec := do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/auth/sessions?limit=1", "", bearer(memberToken))
	var body LoginSessionCollection
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(body.Items) != 1 || body.Items[0].Current || body.Items[0].LastSeenAt.Valid || body.CurrentSession.Value == nil || !body.CurrentSession.Value.Current || !body.CurrentSession.Value.LastSeenAt.Valid {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = do(t, newTestHandler(t, deps), "GET", Prefix+"/auth/sessions", "", bearer(apiKeyToken))
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || body.CurrentSession.Value != nil {
		t.Fatalf("API key current session: %d %s", rec.Code, rec.Body)
	}
}
