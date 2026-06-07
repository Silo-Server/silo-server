package jellycompat

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
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

type stubFolderSource struct {
	enabled       []*models.MediaFolder
	byIDs         []*models.MediaFolder
	listByIDsArgs []int
}

func (s *stubFolderSource) GetEnabled(context.Context) ([]*models.MediaFolder, error) {
	return s.enabled, nil
}

func (s *stubFolderSource) ListByIDs(_ context.Context, ids []int) ([]*models.MediaFolder, error) {
	s.listByIDsArgs = ids
	return s.byIDs, nil
}

func TestListUserLibrariesRestrictedAllowlist(t *testing.T) {
	folders := &stubFolderSource{
		byIDs: []*models.MediaFolder{
			{ID: 2, Name: "TV Shows", Type: "series"},
			{ID: 19, Name: "Movies", Type: "movies"},
		},
	}
	svc := &directContentService{
		folderRepo: folders,
		accessFilter: NewScopeAccessFilter(&stubScopeResolver{
			scope: access.Scope{
				AllowedLibraryIDs:   []int{2, 19},
				LibrariesRestricted: true,
			},
		}),
	}

	libraries, err := svc.ListUserLibraries(context.Background(), &Session{StreamAppUserID: 7, ProfileID: "profile-1"})
	if err != nil {
		t.Fatalf("ListUserLibraries: %v", err)
	}
	if !reflect.DeepEqual(folders.listByIDsArgs, []int{2, 19}) {
		t.Fatalf("ListByIDs called with %v, want [2 19]", folders.listByIDsArgs)
	}
	if len(libraries) != 2 {
		t.Fatalf("got %d libraries, want 2", len(libraries))
	}
}

func TestListUserLibrariesFiltersDisabled(t *testing.T) {
	svc := &directContentService{
		folderRepo: &stubFolderSource{
			enabled: []*models.MediaFolder{
				{ID: 2, Name: "TV Shows", Type: "series"},
				{ID: 7, Name: "XXX", Type: "movies"},
				{ID: 19, Name: "Movies", Type: "movies"},
			},
		},
		accessFilter: NewScopeAccessFilter(&stubScopeResolver{
			scope: access.Scope{
				DisabledLibraryIDs: []int{7},
			},
		}),
	}

	libraries, err := svc.ListUserLibraries(context.Background(), &Session{StreamAppUserID: 1, ProfileID: "profile-1"})
	if err != nil {
		t.Fatalf("ListUserLibraries: %v", err)
	}
	if len(libraries) != 2 {
		t.Fatalf("got %d libraries, want 2 (disabled library not filtered)", len(libraries))
	}
	for _, lib := range libraries {
		if lib.ID == 7 {
			t.Fatal("user-disabled library 7 still present in views list")
		}
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
