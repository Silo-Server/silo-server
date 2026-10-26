package access

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestProfileIsLimited(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile userstore.Profile
		want    bool
	}{
		{"unlimited", userstore.Profile{ID: "p", IsPrimary: true}, false},
		{"pin", userstore.Profile{ID: "p", PINHash: "hash"}, true},
		{"rating ceiling", userstore.Profile{ID: "p", MaxContentRating: "TV-Y7"}, true},
		// A stored blank ceiling blocks everything (see HasCeiling), so it is a
		// restriction, not the absence of one.
		{"blank deny-all ceiling", userstore.Profile{ID: "p", MaxContentRating: " "}, true},
		{"advisory limit", userstore.Profile{ID: "p", MaxAdvisoryAge: 12}, true},
		{"library restriction", userstore.Profile{ID: "p", LibraryRestrictionsEnabled: true, AllowedLibraryIDs: []int{1}}, true},
		{"empty library restriction", userstore.Profile{ID: "p", LibraryRestrictionsEnabled: true}, true},
		// Library IDs without the restriction flag are ignored by the resolver.
		{"library ids without restriction", userstore.Profile{ID: "p", AllowedLibraryIDs: []int{1}}, false},
		// Neither hides a title: the child flag drives presentation and the
		// quality ceiling caps delivery, not what the profile may see.
		{"child flag only", userstore.Profile{ID: "p", IsChild: true}, false},
		{"quality ceiling only", userstore.Profile{ID: "p", MaxPlaybackQuality: "720p"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProfileIsLimited(tc.profile); got != tc.want {
				t.Fatalf("ProfileIsLimited(%+v) = %v, want %v", tc.profile, got, tc.want)
			}
		})
	}
}

func TestHouseholdRequiresProfile(t *testing.T) {
	admin := userstore.Profile{ID: "admin", IsPrimary: true}
	guest := userstore.Profile{ID: "guest"}
	kid := userstore.Profile{ID: "kid", MaxContentRating: "TV-Y7"}
	lockedAdmin := userstore.Profile{ID: "admin", IsPrimary: true, PINHash: "hash"}

	for _, tc := range []struct {
		name     string
		profiles []userstore.Profile
		want     bool
	}{
		{"no profiles", nil, false},
		{"single unlimited profile", []userstore.Profile{admin}, false},
		{"several unlimited profiles", []userstore.Profile{admin, guest}, false},
		{"restricted child profile", []userstore.Profile{admin, kid}, true},
		{"pin-locked parent profile", []userstore.Profile{lockedAdmin, guest}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HouseholdRequiresProfile(tc.profiles); got != tc.want {
				t.Fatalf("HouseholdRequiresProfile = %v, want %v", got, tc.want)
			}
		})
	}
}
