package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
)

// revocationTestGroups knows one access group, so a group move validates.
type revocationTestGroups struct{}

func (revocationTestGroups) Get(_ context.Context, id int64) (*access.Group, error) {
	if id != 5 {
		return nil, access.ErrGroupNotFound
	}
	return &access.Group{ID: id, Name: "Family"}, nil
}

func (revocationTestGroups) List(context.Context) ([]access.Group, error) { return nil, nil }

func (revocationTestGroups) GetPolicyForUser(context.Context, int) (*access.GroupPolicy, error) {
	return nil, nil
}

// Policy changes take effect on the next request and reach connected clients
// through the realtime socket, so only credential, enabled and role changes
// sign the account out.
func TestHandleUpdateUserSignsOutOnlyForCredentialEnabledAndRoleChanges(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantRevoke bool
	}{
		{name: "access group", body: `{"access_group_id":5}`},
		{name: "permissions", body: `{"permissions":["marker_edit"]}`},
		{name: "playback quality override", body: `{"max_playback_quality":"720p"}`},
		{name: "playback quality cleared", body: `{"max_playback_quality":null}`},
		{name: "password", body: `{"password":"a-new-long-password"}`, wantRevoke: true},
		{name: "disable", body: `{"enabled":false}`, wantRevoke: true},
		{name: "role", body: `{"role":"admin"}`, wantRevoke: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, repo := newScopedKeyAdminHandler(models.RoleUser)
			quality := "1080p"
			repo.user.MaxPlaybackQuality = &quality
			h.AccessGroups = revocationTestGroups{}
			var notified []int
			h.OnUserSessionsRevoked = func(_ context.Context, userID int) { notified = append(notified, userID) }

			rec := updateUserRequestFor(t, h, jwtAdminClaims(), tt.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}
			if repo.updated == nil {
				t.Fatal("update was not written")
			}
			if repo.revoked != tt.wantRevoke {
				t.Fatalf("sign-ins revoked = %v, want %v", repo.revoked, tt.wantRevoke)
			}
			if got := len(notified) == 1 && notified[0] == repo.user.ID; got != tt.wantRevoke {
				t.Fatalf("compatibility sessions dropped for %v, want revocation %v", notified, tt.wantRevoke)
			}
		})
	}
}

func TestUpdateAdminAccountSignsOutOnlyForCredentialEnabledAndRoleChanges(t *testing.T) {
	permissions := []string{"marker_edit"}
	quality := "720p"
	password := "a-new-long-password"
	disabled := false
	admin := models.RoleAdmin
	tests := []struct {
		name       string
		input      models.UpdateUserInput
		wantRevoke bool
	}{
		// The v2 group move reads the group inside the account transaction, so
		// that case is covered by TestUpdateRequiresSessionRevocation.
		{name: "permissions", input: models.UpdateUserInput{Permissions: &permissions}},
		{name: "playback quality override", input: models.UpdateUserInput{MaxPlaybackQuality: models.SetValue(quality)}},
		{name: "playback quality cleared", input: models.UpdateUserInput{MaxPlaybackQuality: models.ClearValue[string]()}},
		{name: "password", input: models.UpdateUserInput{Password: &password}, wantRevoke: true},
		{name: "disable", input: models.UpdateUserInput{Enabled: &disabled}, wantRevoke: true},
		{name: "role", input: models.UpdateUserInput{Role: &admin}, wantRevoke: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, repo := newScopedKeyAdminHandler(models.RoleUser)
			current := "1080p"
			repo.user.MaxPlaybackQuality = &current
			var notified []int
			h.OnUserSessionsRevoked = func(_ context.Context, userID int) { notified = append(notified, userID) }

			if _, err := h.UpdateAdminAccount(apimw.SetClaims(context.Background(), jwtAdminClaims()), repo.user.ID, -1, 0, tt.input); err != nil {
				t.Fatalf("UpdateAdminAccount() error = %v", err)
			}
			if repo.revoked != tt.wantRevoke {
				t.Fatalf("sign-ins revoked = %v, want %v", repo.revoked, tt.wantRevoke)
			}
			if got := len(notified) == 1 && notified[0] == repo.user.ID; got != tt.wantRevoke {
				t.Fatalf("compatibility sessions dropped for %v, want revocation %v", notified, tt.wantRevoke)
			}
		})
	}
}
