package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestUpdateRequiresSessionRevocation(t *testing.T) {
	role := "admin"
	sameRole := "user"
	enabled := true
	disabled := false
	libraryIDs := []int{1, 2}
	sameLibraryIDs := []int{1}
	emptyLibraryIDs := []int{}
	maxPlaybackQuality := "1080p"
	sameMaxPlaybackQuality := "original"
	password := "new-password"
	username := "renamed"
	maxStreams := 4
	permissions := []string{"metadata_curation"}
	samePermissions := []string{"download"}
	groupID := int64(5)

	current := &models.User{
		Role:               "user",
		Permissions:        []string{"download"},
		Enabled:            false,
		LibraryIDs:         []int{1},
		MaxPlaybackQuality: &sameMaxPlaybackQuality,
	}

	tests := []struct {
		name string
		in   models.UpdateUserInput
		want bool
	}{
		{
			name: "permissions change does not revoke session",
			in:   models.UpdateUserInput{Permissions: &permissions},
			want: false,
		},
		{
			name: "permissions unchanged",
			in:   models.UpdateUserInput{Permissions: &samePermissions},
			want: false,
		},
		{
			name: "role change does not revoke session",
			in:   models.UpdateUserInput{Role: &role},
			want: false,
		},
		{
			name: "role unchanged",
			in:   models.UpdateUserInput{Role: &sameRole},
			want: false,
		},
		{
			name: "enabled",
			in:   models.UpdateUserInput{Enabled: &enabled},
			want: true,
		},
		{
			name: "enabled unchanged",
			in:   models.UpdateUserInput{Enabled: &disabled},
			want: false,
		},
		{
			name: "library ids does not revoke session",
			in:   models.UpdateUserInput{LibraryIDs: models.SetValue(libraryIDs)},
			want: false,
		},
		{
			name: "library ids unchanged",
			in:   models.UpdateUserInput{LibraryIDs: models.SetValue(sameLibraryIDs)},
			want: false,
		},
		{
			name: "library ids nil does not revoke session",
			in:   models.UpdateUserInput{LibraryIDs: models.ClearValue[[]int]()},
			want: false,
		},
		{
			name: "max playback quality change does not revoke session",
			in:   models.UpdateUserInput{MaxPlaybackQuality: models.SetValue(maxPlaybackQuality)},
			want: false,
		},
		{
			name: "max playback quality unchanged",
			in:   models.UpdateUserInput{MaxPlaybackQuality: models.SetValue(sameMaxPlaybackQuality)},
			want: false,
		},
		{
			name: "max playback quality cleared to inherit does not revoke session",
			in:   models.UpdateUserInput{MaxPlaybackQuality: models.ClearValue[string]()},
			want: false,
		},
		{
			name: "password",
			in:   models.UpdateUserInput{Password: &password},
			want: true,
		},
		{
			name: "role with policy changes does not revoke session",
			in:   models.UpdateUserInput{Role: &role, Permissions: &permissions, AccessGroupID: models.SetValue(groupID)},
			want: false,
		},
		{
			name: "access group change does not revoke session",
			in:   models.UpdateUserInput{AccessGroupID: models.SetValue(groupID)},
			want: false,
		},
		{
			name: "access group unchanged",
			in:   models.UpdateUserInput{AccessGroupID: models.ClearValue[int64]()},
			want: false,
		},
		{
			name: "non access fields",
			in:   models.UpdateUserInput{Username: &username, MaxStreams: models.SetValue(maxStreams)},
			want: false,
		},
		{
			name: "empty update",
			in:   models.UpdateUserInput{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := updateRequiresSessionRevocation(current, tt.in); got != tt.want {
				t.Fatalf("updateRequiresSessionRevocation() = %v, want %v", got, tt.want)
			}
		})
	}

	inheritingCurrent := *current
	inheritingCurrent.MaxPlaybackQuality = nil
	t.Run("max playback quality inherit unchanged", func(t *testing.T) {
		if got := updateRequiresSessionRevocation(&inheritingCurrent, models.UpdateUserInput{MaxPlaybackQuality: models.ClearValue[string]()}); got {
			t.Fatalf("updateRequiresSessionRevocation() = %v, want false", got)
		}
	})

	unrestrictedCurrent := *current
	unrestrictedCurrent.LibraryIDs = nil
	t.Run("library ids empty does not revoke session", func(t *testing.T) {
		if got := updateRequiresSessionRevocation(&unrestrictedCurrent, models.UpdateUserInput{LibraryIDs: models.SetValue(emptyLibraryIDs)}); got {
			t.Fatalf("updateRequiresSessionRevocation() = %v, want false", got)
		}
	})

	t.Run("library ids nil unchanged", func(t *testing.T) {
		if got := updateRequiresSessionRevocation(&unrestrictedCurrent, models.UpdateUserInput{LibraryIDs: models.ClearValue[[]int]()}); got {
			t.Fatalf("updateRequiresSessionRevocation() = %v, want false", got)
		}
	})

	enabledCurrent := *current
	enabledCurrent.Enabled = true
	t.Run("disable", func(t *testing.T) {
		if got := updateRequiresSessionRevocation(&enabledCurrent, models.UpdateUserInput{Enabled: &disabled}); !got {
			t.Fatalf("updateRequiresSessionRevocation() = %v, want true", got)
		}
	})

	// Without the current account the rule cannot compare values, so it signs
	// the user out for any credential or enabled field and nothing else.
	withoutCurrent := []struct {
		name string
		in   models.UpdateUserInput
		want bool
	}{
		{name: "password", in: models.UpdateUserInput{Password: &password}, want: true},
		{name: "role", in: models.UpdateUserInput{Role: &sameRole}, want: false},
		{name: "enabled", in: models.UpdateUserInput{Enabled: &enabled}, want: true},
		{name: "permissions", in: models.UpdateUserInput{Permissions: &permissions}, want: false},
		{name: "max playback quality", in: models.UpdateUserInput{MaxPlaybackQuality: models.SetValue(maxPlaybackQuality)}, want: false},
		{name: "access group", in: models.UpdateUserInput{AccessGroupID: models.SetValue(groupID)}, want: false},
	}
	for _, tt := range withoutCurrent {
		t.Run("without current "+tt.name, func(t *testing.T) {
			if got := updateRequiresSessionRevocation(nil, tt.in); got != tt.want {
				t.Fatalf("updateRequiresSessionRevocation(nil) = %v, want %v", got, tt.want)
			}
		})
	}
}
