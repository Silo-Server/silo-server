package handlers

import (
	"context"
	"errors"
	"slices"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeAdminLoginSessionStore knows one login session, which revoking by ID
// succeeds for when the account matches, and fails with a missing session
// otherwise. A non-nil revokeErr fails every revocation; revokes counts the
// revocations that ran.
type fakeAdminLoginSessionStore struct {
	session   *models.AuthSession
	revokeErr error
	revokes   *int
}

func (fakeAdminLoginSessionStore) ListByUserPage(context.Context, int, *auth.SessionKey, int) ([]*models.AuthSession, error) {
	return nil, nil
}

func (f fakeAdminLoginSessionStore) RevokeAsAdmin(_ context.Context, _, userID int, sessionID *string) (int, error) {
	if f.revokes != nil {
		*f.revokes++
	}
	if f.revokeErr != nil {
		return 0, f.revokeErr
	}
	if sessionID != nil && (f.session == nil || f.session.ID != *sessionID || f.session.UserID != userID) {
		return 0, auth.ErrSessionNotFound
	}
	return 1, nil
}

type recordedDirectRevocation struct {
	userID    int
	sessionID string
}

type recordingDirectRevoker struct{ sessions []recordedDirectRevocation }

func (r *recordingDirectRevoker) RevokeSession(_ context.Context, userID int, sessionID string) {
	r.sessions = append(r.sessions, recordedDirectRevocation{userID, sessionID})
}

func (r *recordingDirectRevoker) RevokeProfileDevice(context.Context, int, string, string) {}

func (r *recordingDirectRevoker) RevokeProfile(context.Context, int, string) {}

// Signing an account out everywhere ends its Jellyfin-compatible sessions
// too, as a password reset or disable does; signing out one login session
// leaves them alone, since they are not tied to a login session.
func TestRevokeAdminLoginSessionsEndsCompatSessionsOnlyEverywhere(t *testing.T) {
	h, _ := newScopedKeyAdminHandler(models.RoleUser)
	one := "00000000-0000-0000-0000-000000000001"
	h.loginSessions = fakeAdminLoginSessionStore{session: &models.AuthSession{ID: one, UserID: 42}}
	var notified []int
	h.OnUserSessionsRevoked = func(_ context.Context, userID int) { notified = append(notified, userID) }
	ctx := apimw.SetClaims(context.Background(), jwtAdminClaims())

	if _, err := h.RevokeAdminLoginSessions(ctx, 42, &one); err != nil {
		t.Fatal(err)
	}
	if len(notified) != 0 {
		t.Fatalf("single-session revocation notified %v", notified)
	}
	if _, err := h.RevokeAdminLoginSessions(ctx, 42, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(notified, []int{42}) {
		t.Fatalf("notified = %v, want [42]", notified)
	}
}

// An administrator ending one login session withdraws the direct-connection
// entries that session issued, as the account's own session delete does. A
// session of another account, an unknown session, a refused revocation and a
// sign-out of every session (the account-wide hook withdraws those) withdraw
// nothing here.
func TestRevokeAdminLoginSessionWithdrawsEntriesTheSessionIssued(t *testing.T) {
	const sessionID = "00000000-0000-0000-0000-000000000001"
	tests := []struct {
		name      string
		session   models.AuthSession
		sessionID *string
		revokeErr error
		want      []recordedDirectRevocation
		wantErr   bool
	}{
		{name: "login session", session: models.AuthSession{ID: sessionID, UserID: 42}, sessionID: new(sessionID), want: []recordedDirectRevocation{{42, sessionID}}},
		{name: "login session with a device ID", session: models.AuthSession{ID: sessionID, UserID: 42, DeviceID: "tv"}, sessionID: new(sessionID), want: []recordedDirectRevocation{{42, sessionID}}},
		{name: "another account's session", session: models.AuthSession{ID: sessionID, UserID: 43}, sessionID: new(sessionID), wantErr: true},
		{name: "unknown session", session: models.AuthSession{ID: sessionID, UserID: 42}, sessionID: new("00000000-0000-0000-0000-000000000002"), wantErr: true},
		{name: "revocation fails", session: models.AuthSession{ID: sessionID, UserID: 42}, sessionID: new(sessionID), revokeErr: errors.New("connection reset"), wantErr: true},
		{name: "every session of the account", session: models.AuthSession{ID: sessionID, UserID: 42}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newScopedKeyAdminHandler(models.RoleUser)
			var revokes int
			h.loginSessions = fakeAdminLoginSessionStore{session: &tt.session, revokeErr: tt.revokeErr, revokes: &revokes}
			revoker := &recordingDirectRevoker{}
			h.DirectRevoker = revoker

			_, err := h.RevokeAdminLoginSessions(apimw.SetClaims(context.Background(), jwtAdminClaims()), 42, tt.sessionID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, want error %v", err, tt.wantErr)
			}
			if revokes != 1 {
				t.Fatalf("revocations = %d, want 1", revokes)
			}
			if !slices.Equal(revoker.sessions, tt.want) {
				t.Fatalf("withdrawn sessions = %v, want %v", revoker.sessions, tt.want)
			}
		})
	}
}
