package handlers

import "testing"

func TestUpdateRequiresSessionRevocation(t *testing.T) {
	role := "admin"
	enabled := true
	libraryIDs := []int{1, 2}
	maxPlaybackQuality := "1080p"
	password := "new-password"
	username := "renamed"
	maxStreams := 4

	tests := []struct {
		name string
		req  updateUserRequest
		want bool
	}{
		{
			name: "permissions set",
			req:  updateUserRequest{Permissions: updateStringSliceField{Set: true, Value: []string{"metadata_curation"}}},
			want: true,
		},
		{
			name: "permissions unset",
			req:  updateUserRequest{Permissions: updateStringSliceField{Set: false, Value: []string{"metadata_curation"}}},
			want: false,
		},
		{
			name: "role",
			req:  updateUserRequest{Role: &role},
			want: true,
		},
		{
			name: "enabled",
			req:  updateUserRequest{Enabled: &enabled},
			want: true,
		},
		{
			name: "library ids",
			req:  updateUserRequest{LibraryIDs: updateLibraryIDsField{Set: true, Value: libraryIDs}},
			want: true,
		},
		{
			name: "max playback quality",
			req:  updateUserRequest{MaxPlaybackQuality: &maxPlaybackQuality},
			want: true,
		},
		{
			name: "password",
			req:  updateUserRequest{Password: &password},
			want: true,
		},
		{
			name: "non access fields",
			req:  updateUserRequest{Username: &username, MaxStreams: &maxStreams},
			want: false,
		},
		{
			name: "empty update",
			req:  updateUserRequest{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := updateRequiresSessionRevocation(tt.req); got != tt.want {
				t.Fatalf("updateRequiresSessionRevocation() = %v, want %v", got, tt.want)
			}
		})
	}
}
