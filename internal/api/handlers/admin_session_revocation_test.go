package handlers

import (
	"context"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestUpdateAdminAccountSignsOutOnlyForCredentialAndEnabledChanges(t *testing.T) {
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
		{name: "role", input: models.UpdateUserInput{Role: &admin}},
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
