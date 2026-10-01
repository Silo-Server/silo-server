package userdb

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The SQLite backend stores the strict advisory flag the same way Postgres
// does: set on create, changed by an update that names it, left alone by one
// that does not.
func TestSQLiteProfileRequireAdvisoryAgeRoundTrips(t *testing.T) {
	ctx := t.Context()
	store := newConformanceStore(t)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "kid", Name: "Kid", MaxAdvisoryAge: 10, RequireAdvisoryAge: true}); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListProfiles(ctx)
	if err != nil || len(listed) != 1 || !listed[0].RequireAdvisoryAge {
		t.Fatalf("ListProfiles = %+v, %v; want one profile requiring an advisory age", listed, err)
	}

	off := false
	if err := store.UpdateProfile(ctx, "kid", userstore.UpdateProfileInput{RequireAdvisoryAge: &off}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetProfile(ctx, "kid"); err != nil || got == nil || got.RequireAdvisoryAge {
		t.Fatalf("after clearing = %+v, %v; want false", got, err)
	}

	on := true
	if err := store.UpdateProfile(ctx, "kid", userstore.UpdateProfileInput{RequireAdvisoryAge: &on}); err != nil {
		t.Fatal(err)
	}
	name := "Kiddo"
	if err := store.UpdateProfile(ctx, "kid", userstore.UpdateProfileInput{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetProfile(ctx, "kid"); err != nil || got == nil || !got.RequireAdvisoryAge {
		t.Fatalf("after set and an unrelated update = %+v, %v; want true", got, err)
	}
}
