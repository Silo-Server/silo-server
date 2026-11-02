package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

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
