package access

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestApplyGroupPolicyNoGroupMirrorsUser(t *testing.T) {
	user := &models.User{
		ID:                       7,
		LibraryIDs:               []int{3, 1, 3},
		MaxPlaybackQuality:       "2160P",
		DownloadAllowed:          false,
		DownloadTranscodeAllowed: true,
		MaxStreams:               6,
		MaxTranscodes:            2,
		Permissions:              []string{"metadata_curation", "marker_edit", "marker_edit"},
	}
	got := ApplyGroupPolicy(user, nil)
	want := EffectiveUserPolicy{
		LibraryIDs:               []int{3, 1, 3},
		MaxPlaybackQuality:       "2160P",
		DownloadAllowed:          false,
		DownloadTranscodeAllowed: true,
		MaxStreams:               6,
		MaxTranscodes:            2,
		Permissions:              []string{"metadata_curation", "marker_edit", "marker_edit"},
		RequestsAllowed:          true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ApplyGroupPolicy(no group) = %#v, want %#v", got, want)
	}
}

func TestApplyGroupPolicyRules(t *testing.T) {
	tests := []struct {
		name  string
		user  *models.User
		group *GroupPolicy
		want  EffectiveUserPolicy
	}{
		{
			name: "group libraries restrict unrestricted user",
			user: &models.User{DownloadAllowed: true, DownloadTranscodeAllowed: true},
			group: &GroupPolicy{
				LibraryIDs:               []int{4, 2, 4},
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				RequestsAllowed:          true,
			},
			want: EffectiveUserPolicy{
				LibraryIDs:               []int{2, 4},
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				RequestsAllowed:          true,
			},
		},
		{
			name: "user libraries pass through unrestricted group",
			user: &models.User{
				LibraryIDs:               []int{5, 1},
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
			},
			group: &GroupPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				RequestsAllowed:          true,
			},
			want: EffectiveUserPolicy{
				LibraryIDs:               []int{5, 1},
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				RequestsAllowed:          true,
			},
		},
		{
			name: "libraries intersect with empty boundary",
			user: &models.User{
				LibraryIDs:               []int{1},
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
			},
			group: &GroupPolicy{
				LibraryIDs:               []int{2},
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				RequestsAllowed:          true,
			},
			want: EffectiveUserPolicy{
				LibraryIDs:               []int{},
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				RequestsAllowed:          true,
			},
		},
		{
			name: "quality and booleans use strictest values",
			user: &models.User{
				MaxPlaybackQuality:       "4k",
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
			},
			group: &GroupPolicy{
				MaxPlaybackQuality:       "standard",
				DownloadAllowed:          false,
				DownloadTranscodeAllowed: true,
				RequestsAllowed:          false,
			},
			want: EffectiveUserPolicy{
				MaxPlaybackQuality:       PlaybackQualityStandard,
				DownloadAllowed:          false,
				DownloadTranscodeAllowed: true,
				RequestsAllowed:          false,
			},
		},
		{
			name: "zero limits inherit positive layer",
			user: &models.User{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				MaxStreams:               0,
				MaxTranscodes:            3,
			},
			group: &GroupPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				MaxStreams:               4,
				MaxTranscodes:            0,
				RequestsAllowed:          true,
			},
			want: EffectiveUserPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				MaxStreams:               4,
				MaxTranscodes:            3,
				RequestsAllowed:          true,
			},
		},
		{
			name: "positive limits pick min",
			user: &models.User{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				MaxStreams:               6,
				MaxTranscodes:            2,
			},
			group: &GroupPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				MaxStreams:               4,
				MaxTranscodes:            5,
				RequestsAllowed:          true,
			},
			want: EffectiveUserPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				MaxStreams:               4,
				MaxTranscodes:            2,
				RequestsAllowed:          true,
			},
		},
		{
			name: "permissions intersect sorted deduped",
			user: &models.User{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				Permissions:              []string{"metadata_curation", "marker_edit", "marker_edit"},
			},
			group: &GroupPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				AllowedPermissions:       []string{"marker_edit", "marker_edit"},
				RequestsAllowed:          true,
			},
			want: EffectiveUserPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				Permissions:              []string{"marker_edit"},
				RequestsAllowed:          true,
			},
		},
		{
			name: "empty permission mask removes all",
			user: &models.User{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				Permissions:              []string{"marker_edit"},
			},
			group: &GroupPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				AllowedPermissions:       []string{},
				RequestsAllowed:          true,
			},
			want: EffectiveUserPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				Permissions:              []string{},
				RequestsAllowed:          true,
			},
		},
		{
			name: "nil permission mask leaves user set unchanged",
			user: &models.User{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				Permissions:              []string{"metadata_curation", "marker_edit", "marker_edit"},
			},
			group: &GroupPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				AllowedPermissions:       nil,
				RequestsAllowed:          true,
			},
			want: EffectiveUserPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				Permissions:              []string{"metadata_curation", "marker_edit", "marker_edit"},
				RequestsAllowed:          true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyGroupPolicy(tt.user, tt.group)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ApplyGroupPolicy() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
