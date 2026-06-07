package jellycompat

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

type stubScopeResolver struct {
	scope access.Scope
	err   error
	input access.ResolveInput
}

func (s *stubScopeResolver) Resolve(_ context.Context, input access.ResolveInput) (access.Scope, error) {
	s.input = input
	return s.scope, s.err
}

func TestScopeAccessFilterMapsScope(t *testing.T) {
	resolver := &stubScopeResolver{
		scope: access.Scope{
			UserID:              7,
			ProfileID:           "profile-1",
			AllowedLibraryIDs:   []int{2, 19},
			LibrariesRestricted: true,
			MaxContentRating:    "PG-13",
			MaxPlaybackQuality:  "1080p",
		},
	}

	filter := NewScopeAccessFilter(resolver)(context.Background(), 7, "profile-1")

	if !resolver.input.SkipPINVerification {
		t.Fatal("expected SkipPINVerification=true (PIN is verified at compat login)")
	}
	if resolver.input.UserID != 7 || resolver.input.ProfileID != "profile-1" {
		t.Fatalf("unexpected resolve input: %+v", resolver.input)
	}
	if !reflect.DeepEqual(filter.AllowedLibraryIDs, []int{2, 19}) {
		t.Fatalf("AllowedLibraryIDs = %v, want [2 19]", filter.AllowedLibraryIDs)
	}
	if filter.MaxContentRating != "PG-13" {
		t.Fatalf("MaxContentRating = %q, want PG-13", filter.MaxContentRating)
	}
	if filter.MaxPlaybackQuality != "1080p" {
		t.Fatalf("MaxPlaybackQuality = %q, want 1080p", filter.MaxPlaybackQuality)
	}
	if filter.UserID != 7 || filter.ProfileID != "profile-1" {
		t.Fatalf("identity not propagated: %+v", filter)
	}
}

func TestScopeAccessFilterUnrestrictedScope(t *testing.T) {
	resolver := &stubScopeResolver{
		scope: access.Scope{
			UserID:             1,
			DisabledLibraryIDs: []int{7},
		},
	}

	filter := NewScopeAccessFilter(resolver)(context.Background(), 1, "profile-1")

	if filter.AllowedLibraryIDs != nil {
		t.Fatalf("AllowedLibraryIDs = %v, want nil (unrestricted)", filter.AllowedLibraryIDs)
	}
	if !reflect.DeepEqual(filter.DisabledLibraryIDs, []int{7}) {
		t.Fatalf("DisabledLibraryIDs = %v, want [7]", filter.DisabledLibraryIDs)
	}
}

func TestScopeAccessFilterFailsClosed(t *testing.T) {
	resolver := &stubScopeResolver{err: errors.New("boom")}

	filter := NewScopeAccessFilter(resolver)(context.Background(), 7, "profile-1")

	if filter.AllowedLibraryIDs == nil || len(filter.AllowedLibraryIDs) != 0 {
		t.Fatalf("AllowedLibraryIDs = %v, want empty non-nil allowlist (deny all)", filter.AllowedLibraryIDs)
	}
}

func TestListUserLibrariesEmptyAllowlistDeniesAll(t *testing.T) {
	svc := &directContentService{
		accessFilter: NewScopeAccessFilter(&stubScopeResolver{
			scope: access.Scope{
				AllowedLibraryIDs:   []int{},
				LibrariesRestricted: true,
			},
		}),
	}

	libraries, err := svc.ListUserLibraries(context.Background(), &Session{StreamAppUserID: 7, ProfileID: "profile-1"})
	if err != nil {
		t.Fatalf("ListUserLibraries: %v", err)
	}
	if len(libraries) != 0 {
		t.Fatalf("got %d libraries, want 0 for empty allowlist", len(libraries))
	}
}
