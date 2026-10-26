package usercollections

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

type recordingScopeResolver struct {
	inputs []access.ResolveInput
	scope  access.Scope
	err    error
}

func (r *recordingScopeResolver) Resolve(_ context.Context, input access.ResolveInput) (access.Scope, error) {
	r.inputs = append(r.inputs, input)
	return r.scope, r.err
}

func TestOwnerAccessResolvesTheOwnersContentAccess(t *testing.T) {
	resolver := &recordingScopeResolver{scope: access.Scope{
		UserID:             7,
		ProfileID:          "owner",
		AllowedLibraryIDs:  []int{1, 2},
		DisabledLibraryIDs: []int{9},
		MaturityLimits:     access.MaturityLimits{MaxContentRating: "PG", MaxAdvisoryAge: 10, RequireAdvisoryAge: true},
		MaxPlaybackQuality: "480p",
	}}
	got, err := NewOwnerAccess(resolver).OwnerFilter(t.Context(), 7, "owner")
	if err != nil {
		t.Fatal(err)
	}
	want := catalog.AccessFilter{
		AllowedLibraryIDs: []int{1, 2},
		MaturityLimits:    access.MaturityLimits{MaxContentRating: "PG", MaxAdvisoryAge: 10, RequireAdvisoryAge: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OwnerFilter = %+v, want only the owner's libraries and maturity limits %+v", got, want)
	}
	// The owner is not the requester: no PIN applies, and its hidden
	// libraries are a browsing preference rather than access.
	wantInput := access.ResolveInput{UserID: 7, ProfileID: "owner", SkipPINVerification: true, ContentAccessOnly: true}
	if !reflect.DeepEqual(resolver.inputs, []access.ResolveInput{wantInput}) {
		t.Fatalf("resolve inputs = %+v, want %+v", resolver.inputs, wantInput)
	}
}

func TestOwnerAccessResolvesOnEveryCall(t *testing.T) {
	resolver := &recordingScopeResolver{scope: access.Scope{AllowedLibraryIDs: []int{1}}}
	owners := NewOwnerAccess(resolver)
	for range 2 {
		if _, err := owners.OwnerFilter(t.Context(), 7, "owner"); err != nil {
			t.Fatal(err)
		}
	}
	// No cross-request cache: an owner's access change must apply on the
	// next read on every node.
	if len(resolver.inputs) != 2 {
		t.Fatalf("resolved %d times, want 2", len(resolver.inputs))
	}
}

func TestOwnerAccessFailsClosed(t *testing.T) {
	if _, err := NewOwnerAccess(&recordingScopeResolver{err: access.ErrProfileNotFound}).OwnerFilter(t.Context(), 7, "gone"); !errors.Is(err, access.ErrProfileNotFound) {
		t.Fatalf("err = %v, want the resolver's error", err)
	}
	if _, err := NewOwnerAccess(nil).OwnerFilter(t.Context(), 7, "owner"); err == nil {
		t.Fatal("an owner access without a resolver answered a filter")
	}
	var nilOwners *OwnerAccess
	if _, err := nilOwners.OwnerFilter(t.Context(), 7, "owner"); err == nil {
		t.Fatal("a nil owner access answered a filter")
	}
}
