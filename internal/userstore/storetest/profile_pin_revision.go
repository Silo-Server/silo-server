package storetest

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunProfilePINRevision pins the profile PIN revision that profile tokens are
// bound to: setting, changing or clearing a profile's PIN advances that
// profile's revision, and nothing else does — not the profile's other fields,
// and not any edit to another profile on the account.
func RunProfilePINRevision(t *testing.T, newStore func(*testing.T) userstore.UserStore) {
	store := newStore(t)
	ctx := t.Context()
	for _, p := range []userstore.Profile{
		{ID: "parent", Name: "Parent"},
		{ID: "kid", Name: "Kid"},
	} {
		if err := store.CreateProfile(ctx, p); err != nil {
			t.Fatalf("CreateProfile(%s): %v", p.ID, err)
		}
	}
	want := map[string]int64{"parent": 0, "kid": 0}
	check := func(step string) {
		t.Helper()
		for id, rev := range want {
			p, err := store.GetProfile(ctx, id)
			if err != nil || p == nil {
				t.Fatalf("%s: GetProfile(%s): profile=%v err=%v", step, id, p, err)
			}
			if p.PINRevision != rev {
				t.Fatalf("%s: %s PINRevision = %d, want %d", step, id, p.PINRevision, rev)
			}
		}
		list, err := store.ListProfiles(ctx)
		if err != nil {
			t.Fatalf("%s: ListProfiles: %v", step, err)
		}
		for _, p := range list {
			if p.PINRevision != want[p.ID] {
				t.Fatalf("%s: listed %s PINRevision = %d, want %d", step, p.ID, p.PINRevision, want[p.ID])
			}
		}
	}
	update := func(id string, u userstore.UpdateProfileInput) {
		t.Helper()
		if err := store.UpdateProfile(ctx, id, u); err != nil {
			t.Fatalf("UpdateProfile(%s): %v", id, err)
		}
	}
	str := func(s string) *string { return &s }
	yes := true
	age := 12
	libs := []int{}

	check("created")

	update("parent", userstore.UpdateProfileInput{PIN: str("1234")})
	want["parent"] = 1
	check("parent sets its PIN")

	// Every access-policy field of another profile, the edits a household
	// parent makes while managing a child.
	update("kid", userstore.UpdateProfileInput{
		IsChild:                    &yes,
		MaxContentRating:           str("PG"),
		MaxAdvisoryAge:             &age,
		RequireAdvisoryAge:         &yes,
		LibraryRestrictionsEnabled: &yes,
		AllowedLibraryIDs:          &libs,
		MaxPlaybackQuality:         str("720p"),
		Name:                       str("Kiddo"),
	})
	check("parent edits the kid's limits")

	update("kid", userstore.UpdateProfileInput{PIN: str("9999")})
	want["kid"] = 1
	check("kid's PIN set")

	// The profile's own non-PIN fields, including its limits.
	update("parent", userstore.UpdateProfileInput{
		Name:               str("Mum"),
		MaxContentRating:   str("R"),
		MaxPlaybackQuality: str("2160p"),
		SubtitleMode:       str("always"),
	})
	check("parent edits its own non-PIN fields")

	update("parent", userstore.UpdateProfileInput{PIN: str("4321")})
	want["parent"] = 2
	check("parent changes its PIN")

	update("parent", userstore.UpdateProfileInput{PIN: str("")})
	want["parent"] = 3
	check("parent clears its PIN")

	// pin_hash does not change, so no token can be stale.
	update("parent", userstore.UpdateProfileInput{PIN: str("")})
	check("parent clears its already empty PIN")
}
