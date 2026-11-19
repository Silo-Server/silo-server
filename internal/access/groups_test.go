package access

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func ptr[T any](value T) *T { return &value }

func TestApplyGroupPolicyNoGroupUsesOverridesOverPermissiveDefault(t *testing.T) {
	user := &models.User{
		ID:                       7,
		LibraryIDs:               []int{3, 1, 3},
		MaxPlaybackQuality:       ptr("2160P"),
		DownloadAllowed:          ptr(false),
		DownloadTranscodeAllowed: ptr(true),
		MaxStreams:               ptr(6),
		MaxTranscodes:            ptr(2),
		Permissions:              []string{"metadata_curation", "marker_edit", "marker_edit"},
	}
	got := ApplyGroupPolicy(user, nil)
	want := EffectiveUserPolicy{
		LibraryIDs:               []int{1, 3},
		MaxPlaybackQuality:       PlaybackQuality4K,
		DownloadAllowed:          false,
		DownloadTranscodeAllowed: true,
		TranscodeAllowed:         true,
		AudioTranscodeAllowed:    true,
		MaxStreams:               6,
		MaxTranscodes:            2,
		MaxProfiles:              DefaultMaxProfiles,
		Permissions:              []string{"metadata_curation", "marker_edit", "marker_edit"},
		RequestsAllowed:          true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ApplyGroupPolicy(no group) = %#v, want %#v", got, want)
	}
}

func TestApplyGroupPolicyRules(t *testing.T) {
	restrictive := &GroupPolicy{
		ID:                       9,
		LibraryIDs:               []int{4, 2, 4},
		MaxPlaybackQuality:       "standard",
		DownloadAllowed:          false,
		DownloadTranscodeAllowed: false,
		TranscodeAllowed:         false,
		AudioTranscodeAllowed:    true,
		MaxStreams:               4,
		MaxTranscodes:            1,
		AllowedPermissions:       nil,
		RequestsAllowed:          false,
	}
	inherited := EffectiveUserPolicy{
		LibraryIDs:               []int{2, 4},
		MaxPlaybackQuality:       PlaybackQualityStandard,
		DownloadAllowed:          false,
		DownloadTranscodeAllowed: false,
		TranscodeAllowed:         false,
		AudioTranscodeAllowed:    true,
		MaxStreams:               4,
		MaxTranscodes:            1,
		Permissions:              nil,
		RequestsAllowed:          false,
	}

	tests := []struct {
		name  string
		user  *models.User
		group *GroupPolicy
		want  EffectiveUserPolicy
	}{
		{
			name:  "fully unset user inherits every group field",
			user:  &models.User{},
			group: restrictive,
			want:  inherited,
		},
		{
			name: "grant overrides beat a restrictive group",
			user: &models.User{
				DownloadAllowed:          ptr(true),
				DownloadTranscodeAllowed: ptr(true),
				TranscodeAllowed:         ptr(true),
				RequestsAllowed:          ptr(true),
			},
			group: restrictive,
			want: func() EffectiveUserPolicy {
				want := inherited
				want.DownloadAllowed = true
				want.DownloadTranscodeAllowed = true
				want.TranscodeAllowed = true
				want.RequestsAllowed = true
				return want
			}(),
		},
		{
			name: "restrict overrides beat a permissive group",
			user: &models.User{
				DownloadAllowed:       ptr(false),
				AudioTranscodeAllowed: ptr(false),
				MaxStreams:            ptr(1),
			},
			group: &GroupPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				TranscodeAllowed:         true,
				AudioTranscodeAllowed:    true,
				MaxStreams:               0,
				RequestsAllowed:          true,
			},
			want: EffectiveUserPolicy{
				DownloadAllowed:          false,
				DownloadTranscodeAllowed: true,
				TranscodeAllowed:         true,
				AudioTranscodeAllowed:    false,
				MaxStreams:               1,
				MaxTranscodes:            0,
				RequestsAllowed:          true,
			},
		},
		{
			name: "positive cap above the group cap wins outright",
			user: &models.User{
				MaxStreams:    ptr(6),
				MaxTranscodes: ptr(2),
			},
			group: restrictive,
			want: func() EffectiveUserPolicy {
				want := inherited
				want.MaxStreams = 6
				want.MaxTranscodes = 2
				return want
			}(),
		},
		{
			name:  "zero is an explicit unlimited override",
			user:  &models.User{MaxStreams: ptr(0)},
			group: restrictive,
			want: func() EffectiveUserPolicy {
				want := inherited
				want.MaxStreams = 0
				return want
			}(),
		},
		{
			name:  "negative overrides clamp to unlimited",
			user:  &models.User{MaxTranscodes: ptr(-3)},
			group: restrictive,
			want: func() EffectiveUserPolicy {
				want := inherited
				want.MaxTranscodes = 0
				return want
			}(),
		},
		{
			name:  "quality override replaces the group ceiling",
			user:  &models.User{MaxPlaybackQuality: ptr("4k")},
			group: restrictive,
			want: func() EffectiveUserPolicy {
				want := inherited
				want.MaxPlaybackQuality = PlaybackQuality4K
				return want
			}(),
		},
		{
			name:  "empty quality override means no ceiling",
			user:  &models.User{MaxPlaybackQuality: ptr("")},
			group: restrictive,
			want: func() EffectiveUserPolicy {
				want := inherited
				want.MaxPlaybackQuality = ""
				return want
			}(),
		},
		{
			name:  "library override replaces the group list without intersecting",
			user:  &models.User{LibraryIDs: []int{5, 1, 5}},
			group: restrictive,
			want: func() EffectiveUserPolicy {
				want := inherited
				want.LibraryIDs = []int{1, 5}
				return want
			}(),
		},
		{
			name:  "empty library override restricts to nothing",
			user:  &models.User{LibraryIDs: []int{}},
			group: restrictive,
			want: func() EffectiveUserPolicy {
				want := inherited
				want.LibraryIDs = []int{}
				return want
			}(),
		},
		{
			name: "group libraries apply to a user without an override",
			user: &models.User{},
			group: &GroupPolicy{
				LibraryIDs:               nil,
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				TranscodeAllowed:         true,
				AudioTranscodeAllowed:    true,
				RequestsAllowed:          true,
			},
			want: EffectiveUserPolicy{
				LibraryIDs:               nil,
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				TranscodeAllowed:         true,
				AudioTranscodeAllowed:    true,
				RequestsAllowed:          true,
			},
		},
		{
			name: "permissions intersect sorted deduped",
			user: &models.User{
				Permissions: []string{"metadata_curation", "marker_edit", "marker_edit"},
			},
			group: &GroupPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				TranscodeAllowed:         true,
				AudioTranscodeAllowed:    true,
				AllowedPermissions:       []string{"marker_edit", "marker_edit"},
				RequestsAllowed:          true,
			},
			want: EffectiveUserPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				TranscodeAllowed:         true,
				AudioTranscodeAllowed:    true,
				Permissions:              []string{"marker_edit"},
				RequestsAllowed:          true,
			},
		},
		{
			name: "empty permission mask removes all",
			user: &models.User{
				Permissions: []string{"marker_edit"},
			},
			group: &GroupPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				TranscodeAllowed:         true,
				AudioTranscodeAllowed:    true,
				AllowedPermissions:       []string{},
				RequestsAllowed:          true,
			},
			want: EffectiveUserPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				TranscodeAllowed:         true,
				AudioTranscodeAllowed:    true,
				Permissions:              []string{},
				RequestsAllowed:          true,
			},
		},
		{
			name: "nil permission mask leaves user set unchanged",
			user: &models.User{
				Permissions: []string{"metadata_curation", "marker_edit", "marker_edit"},
			},
			group: &GroupPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				TranscodeAllowed:         true,
				AudioTranscodeAllowed:    true,
				AllowedPermissions:       nil,
				RequestsAllowed:          true,
			},
			want: EffectiveUserPolicy{
				DownloadAllowed:          true,
				DownloadTranscodeAllowed: true,
				TranscodeAllowed:         true,
				AudioTranscodeAllowed:    true,
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

// failingGroupProvider fails the test if the resolver queries it.
type failingGroupProvider struct{ t *testing.T }

func (p failingGroupProvider) GetPolicyForUser(context.Context, int) (*GroupPolicy, error) {
	p.t.Helper()
	p.t.Fatal("GetPolicyForUser should not be called for an account with no access group")
	return nil, nil
}

// An admin row that still carries a group (written before admins were kept
// ungrouped) resolves to the admin defaults without consulting the provider.
func TestEffectivePolicyForUserIgnoresGroupOnAdmin(t *testing.T) {
	groupID := int64(7)
	user := &models.User{ID: 3, Role: models.RoleAdmin, AccessGroupID: &groupID}
	got, err := EffectivePolicyForUser(context.Background(), user, failingGroupProvider{t: t})
	if err != nil {
		t.Fatalf("EffectivePolicyForUser() error = %v", err)
	}
	if want := adminFullAccess(); !reflect.DeepEqual(got, want) {
		t.Fatalf("EffectivePolicyForUser(admin) = %#v, want full access %#v", got, want)
	}
}

func adminFullAccess() EffectiveUserPolicy {
	return EffectiveUserPolicy{
		DownloadAllowed:          true,
		DownloadTranscodeAllowed: true,
		TranscodeAllowed:         true,
		AudioTranscodeAllowed:    true,
		RequestsAllowed:          true,
		MaxProfiles:              DefaultMaxProfiles,
	}
}

// An admin with no overrides has full access, including server-prepared
// downloads, which the no-group defaults for a regular account deny (#1403).
func TestApplyGroupPolicyAdminDefaultsToFullAccess(t *testing.T) {
	admin := &models.User{ID: 1, Role: models.RoleAdmin}
	if got, want := ApplyGroupPolicy(admin, nil), adminFullAccess(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ApplyGroupPolicy(admin) = %#v, want %#v", got, want)
	}
	regular := &models.User{ID: 2, Role: models.RoleUser}
	if ApplyGroupPolicy(regular, nil).DownloadTranscodeAllowed {
		t.Fatal("ApplyGroupPolicy(ungrouped regular).DownloadTranscodeAllowed = true, want the no-group default false")
	}
}

func TestApplyGroupPolicyAdminOverridesStillRestrict(t *testing.T) {
	admin := &models.User{ID: 1, Role: models.RoleAdmin, DownloadTranscodeAllowed: ptr(false), MaxStreams: ptr(2)}
	got := ApplyGroupPolicy(admin, nil)
	if got.DownloadTranscodeAllowed || got.MaxStreams != 2 {
		t.Fatalf("ApplyGroupPolicy(restricted admin) = %#v, want the overrides to win", got)
	}
}

// TestWiderThan: a restore creates an account only when the policy here
// grants nothing the backup's did not, so every field's open value
// (unrestricted libraries, no quality ceiling, 0 caps) counts as wider than
// any limit, and narrower values never do.
func TestWiderThan(t *testing.T) {
	limit := EffectiveUserPolicy{LibraryIDs: []int{1, 2}, MaxPlaybackQuality: "1080p", MaxStreams: 2, MaxTranscodes: 1,
		MaxRemoteStreamBitrateKbps: 8000, MaxLocalStreamBitrateKbps: 0, MaxProfiles: 3, Permissions: []string{"marker_edit"}, RequestsAllowed: true}
	cases := []struct {
		name   string
		change func(*EffectiveUserPolicy)
		want   []string
	}{
		{"same", func(*EffectiveUserPolicy) {}, nil},
		{"narrower everywhere", func(p *EffectiveUserPolicy) {
			p.LibraryIDs, p.MaxStreams, p.MaxTranscodes, p.MaxRemoteStreamBitrateKbps, p.MaxLocalStreamBitrateKbps = []int{}, 1, 1, 4000, 1000
			p.MaxProfiles, p.Permissions, p.RequestsAllowed = 1, nil, false
		}, nil},
		{"libraries unrestricted", func(p *EffectiveUserPolicy) { p.LibraryIDs = nil }, []string{"library_ids"}},
		{"a library added", func(p *EffectiveUserPolicy) { p.LibraryIDs = []int{1, 3} }, []string{"library_ids"}},
		{"quality ceiling lifted", func(p *EffectiveUserPolicy) { p.MaxPlaybackQuality = "" }, []string{"max_playback_quality"}},
		{"quality ceiling raised", func(p *EffectiveUserPolicy) { p.MaxPlaybackQuality = "4K" }, []string{"max_playback_quality"}},
		{"stream cap lifted", func(p *EffectiveUserPolicy) { p.MaxStreams = 0 }, []string{"max_streams"}},
		{"bitrate cap raised", func(p *EffectiveUserPolicy) { p.MaxRemoteStreamBitrateKbps = 9000 }, []string{"max_remote_stream_bitrate_kbps"}},
		{"more profiles", func(p *EffectiveUserPolicy) { p.MaxProfiles = 4 }, []string{"max_profiles"}},
		{"downloads allowed", func(p *EffectiveUserPolicy) { p.DownloadAllowed = true }, []string{"download_allowed"}},
		{"a permission added", func(p *EffectiveUserPolicy) { p.Permissions = []string{"marker_edit", "metadata_curation"} }, []string{"permissions"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			policy := limit
			policy.LibraryIDs = append([]int(nil), limit.LibraryIDs...)
			c.change(&policy)
			var fields []string
			for _, w := range WiderThan(policy, limit) {
				fields = append(fields, w.Field)
			}
			if strings.Join(fields, ",") != strings.Join(c.want, ",") {
				t.Fatalf("WiderThan = %v, want %v", fields, c.want)
			}
		})
	}
	if got := WiderThan(EffectiveUserPolicy{LibraryIDs: []int{5}}, EffectiveUserPolicy{}); len(got) != 0 {
		t.Fatalf("an unrestricted limit has nothing to widen: %+v", got)
	}
}
