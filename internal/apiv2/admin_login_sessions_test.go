package apiv2

import (
	"context"
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
