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
	var allLibraryIDs []int
	maxPlaybackQuality := "1080p"
	sameMaxPlaybackQuality := "original"
	password := "new-password"
	username := "renamed"
	maxStreams := 4
	permissions := []string{"metadata_curation"}
	samePermissions := []string{"download"}

	current := &models.User{
		Role:               "user",
		Permissions:        []string{"download"},
		Enabled:            false,
		LibraryIDs:         []int{1},
		MaxPlaybackQuality: "original",
	}

	tests := []struct {
		name string
		in   models.UpdateUserInput
		want bool
	}{
		{
			name: "permissions set",
			in:   models.UpdateUserInput{Permissions: &permissions},
			want: true,
		},
		{
			name: "permissions unchanged",
			in:   models.UpdateUserInput{Permissions: &samePermissions},
			want: false,
		},
		{
			name: "role",
			in:   models.UpdateUserInput{Role: &role},
			want: true,
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
			name: "library ids",
			in:   models.UpdateUserInput{LibraryIDs: &libraryIDs},
			want: true,
		},
		{
			name: "library ids unchanged",
			in:   models.UpdateUserInput{LibraryIDs: &sameLibraryIDs},
			want: false,
		},
		{
			name: "library ids nil differs from restricted",
			in:   models.UpdateUserInput{LibraryIDs: &allLibraryIDs},
			want: true,
		},
		{
			name: "max playback quality",
			in:   models.UpdateUserInput{MaxPlaybackQuality: &maxPlaybackQuality},
			want: true,
		},
		{
			name: "max playback quality unchanged",
			in:   models.UpdateUserInput{MaxPlaybackQuality: &sameMaxPlaybackQuality},
			want: false,
		},
		{
			name: "password",
			in:   models.UpdateUserInput{Password: &password},
			want: true,
		},
		{
			name: "non access fields",
			in:   models.UpdateUserInput{Username: &username, MaxStreams: &maxStreams},
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

	unrestrictedCurrent := *current
	unrestrictedCurrent.LibraryIDs = nil
	t.Run("library ids empty differs from nil", func(t *testing.T) {
		if got := updateRequiresSessionRevocation(&unrestrictedCurrent, models.UpdateUserInput{LibraryIDs: &emptyLibraryIDs}); !got {
			t.Fatalf("updateRequiresSessionRevocation() = %v, want true", got)
		}
	})

	t.Run("library ids nil unchanged", func(t *testing.T) {
		if got := updateRequiresSessionRevocation(&unrestrictedCurrent, models.UpdateUserInput{LibraryIDs: &allLibraryIDs}); got {
			t.Fatalf("updateRequiresSessionRevocation() = %v, want false", got)
		}
	})
}
