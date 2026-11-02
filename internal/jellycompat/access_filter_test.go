package jellycompat

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync"
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
			UserID:                    7,
			ProfileID:                 "profile-1",
			AllowedLibraryIDs:         []int{2, 19},
			LibrariesRestricted:       true,
			MaturityLimits:            access.MaturityLimits{MaxContentRating: "PG-13", AllowUnratedContent: true, MaxAdvisoryAge: 10},
			MaxPlaybackQuality:        "1080p",
			PreferredMetadataLanguage: "fr",
			MetadataLanguageOverrides: map[string]string{
				"no": access.OriginalMetadataLanguage,
			},
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
	// Every maturity limit reaches the Jellyfin surface, so a compat client
	// cannot see titles the native API hides from the same profile.
	if want := (access.MaturityLimits{MaxContentRating: "PG-13", AllowUnratedContent: true, MaxAdvisoryAge: 10}); filter.MaturityLimits != want {
		t.Fatalf("MaturityLimits = %+v, want %+v", filter.MaturityLimits, want)
	}
	if filter.MaxPlaybackQuality != "1080p" {
		t.Fatalf("MaxPlaybackQuality = %q, want 1080p", filter.MaxPlaybackQuality)
	}
	if filter.ProfilePreferredLanguage != "fr" {
		t.Fatalf("ProfilePreferredLanguage = %q, want fr", filter.ProfilePreferredLanguage)
	}
	if got := filter.MetadataLanguageOverrides["no"]; got != access.OriginalMetadataLanguage {
		t.Fatalf("MetadataLanguageOverrides[no] = %q, want %q", got, access.OriginalMetadataLanguage)
	}
	if filter.UserID != 7 || filter.ProfileID != "profile-1" {
		t.Fatalf("identity not propagated: %+v", filter)
	}
}

func TestScopeAccessFilterFailsClosed(t *testing.T) {
	resolver := &stubScopeResolver{err: errors.New("boom")}

	filter := NewScopeAccessFilter(resolver)(context.Background(), 7, "profile-1")

	if filter.AllowedLibraryIDs == nil || len(filter.AllowedLibraryIDs) != 0 {
		t.Fatalf("AllowedLibraryIDs = %v, want empty non-nil allowlist (deny all)", filter.AllowedLibraryIDs)
	}
}

// countingScopeResolver hands out a fresh scope() per call, or err.
type countingScopeResolver struct {
	mu    sync.Mutex
	calls int
	scope func() access.Scope
	err   error
}

func (r *countingScopeResolver) Resolve(context.Context, access.ResolveInput) (access.Scope, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return access.Scope{}, r.err
	}
	return r.scope(), nil
}

func (r *countingScopeResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// TestRequestScopeResolvesOncePerRequest: within one authenticated compat
// request the access filter and the playback bitrate cap share one scope
// resolution, edits a caller makes to its filter never reach the next caller,
// and anything outside that request's own viewer resolves as before.
func TestRequestScopeResolvesOncePerRequest(t *testing.T) {
	restricted := func() access.Scope {
		return access.Scope{AllowedLibraryIDs: []int{2, 19}, DisabledLibraryIDs: []int{4}, HiddenLibraryIDs: []int{5}, MetadataLanguageOverrides: map[string]string{"no": "en"}}
	}
	session := &Session{StreamAppUserID: 7, ProfileID: "p1"}
	newFilter := func(resolver ScopeResolver) AccessFilterResolver {
		return compatAccessFilterResolver(NewScopeAccessFilter(resolver))
	}

	t.Run("one resolution per request", func(t *testing.T) {
		resolver := &countingScopeResolver{scope: restricted}
		filter, playbackScope := newFilter(resolver), rememberRequestScope(resolver)
		handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			for range 2 {
				got := filter(r.Context(), 7, "p1")
				if !slices.Equal(got.AllowedLibraryIDs, []int{2, 19}) || !slices.Equal(got.DisabledLibraryIDs, []int{4}) || !maps.Equal(got.MetadataLanguageOverrides, map[string]string{"no": "en"}) {
					t.Errorf("filter = %+v, want the resolved scope unchanged", got)
				}
				got.AllowedLibraryIDs[0], got.DisabledLibraryIDs[0], got.MetadataLanguageOverrides["no"] = 99, 99, "fr"
			}
			scope, err := playbackScope.Resolve(r.Context(), compatScopeInput(7, "p1"))
			if err != nil || !slices.Equal(scope.AllowedLibraryIDs, []int{2, 19}) || !slices.Equal(scope.HiddenLibraryIDs, []int{5}) {
				t.Errorf("playback scope = %+v, %v", scope, err)
			}
		})
		serveWithSessionContext(handler, httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), session)
		if got := resolver.count(); got != 1 {
			t.Fatalf("one request resolved %d times, want 1", got)
		}
		serveWithSessionContext(handler, httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), session)
		if got := resolver.count(); got != 2 {
			t.Fatalf("two requests resolved %d times, want 2: the scope must not outlive its request", got)
		}
	})

	t.Run("another viewer resolves on its own", func(t *testing.T) {
		resolver := &countingScopeResolver{scope: restricted}
		filter, ctx := newFilter(resolver), withRequestScope(context.Background(), session)
		for _, profileID := range []string{"p2", "p1", "p2", "p1"} {
			filter(ctx, 7, profileID)
		}
		if got := resolver.count(); got != 3 {
			t.Fatalf("resolved %d times, want 3: only the session's own profile is remembered", got)
		}
		if _, err := rememberRequestScope(resolver).Resolve(ctx, access.ResolveInput{UserID: 7, ProfileID: "p1", SkipPINVerification: true, ContentAccessOnly: true}); err != nil || resolver.count() != 4 {
			t.Fatalf("a different resolve input reused the remembered scope (calls=%d, err=%v)", resolver.count(), err)
		}
	})

	t.Run("a failure is not remembered", func(t *testing.T) {
		resolver := &countingScopeResolver{scope: restricted, err: errors.New("database unavailable")}
		filter, ctx := newFilter(resolver), withRequestScope(context.Background(), session)
		if got := filter(ctx, 7, "p1"); got.AllowedLibraryIDs == nil || len(got.AllowedLibraryIDs) != 0 {
			t.Fatalf("failed resolution AllowedLibraryIDs = %#v, want deny all", got.AllowedLibraryIDs)
		}
		resolver.err = nil
		for range 2 {
			if got := filter(ctx, 7, "p1"); !slices.Equal(got.AllowedLibraryIDs, []int{2, 19}) {
				t.Fatalf("AllowedLibraryIDs after recovery = %#v, want [2 19]", got.AllowedLibraryIDs)
			}
		}
		if got := resolver.count(); got != 2 {
			t.Fatalf("resolved %d times, want 2: retry the failure once, then remember", got)
		}
	})

	// A nil allowlist grants every library and an empty one grants none, so
	// the remembered copy must keep which one it was.
	for name, allowed := range map[string][]int{"unrestricted": nil, "deny all": {}} {
		t.Run(name+" allowlist keeps its meaning", func(t *testing.T) {
			resolver := &countingScopeResolver{scope: func() access.Scope { return access.Scope{AllowedLibraryIDs: slices.Clone(allowed)} }}
			filter, ctx := newFilter(resolver), withRequestScope(context.Background(), session)
			for range 2 {
				if got := filter(ctx, 7, "p1").AllowedLibraryIDs; (got == nil) != (allowed == nil) || len(got) != 0 {
					t.Fatalf("AllowedLibraryIDs = %#v, want %#v", got, allowed)
				}
			}
		})
	}
}

// TestCloneScopeCopiesReferenceFields: every top-level slice, map, and pointer
// field of a remembered scope is copied, including ones added to access.Scope
// later.
func TestCloneScopeCopiesReferenceFields(t *testing.T) {
	var scope access.Scope
	in := reflect.ValueOf(&scope).Elem()
	for i := range in.NumField() {
		field := in.Field(i)
		switch field.Kind() {
		case reflect.Slice:
			field.Set(reflect.MakeSlice(field.Type(), 1, 1))
		case reflect.Map:
			field.Set(reflect.MakeMap(field.Type()))
			field.SetMapIndex(reflect.Zero(field.Type().Key()), reflect.Zero(field.Type().Elem()))
		case reflect.Pointer:
			field.Set(reflect.New(field.Type().Elem()))
		}
	}
	out := reflect.ValueOf(cloneScope(scope))
	for i := range in.NumField() {
		switch in.Field(i).Kind() {
		case reflect.Slice, reflect.Map, reflect.Pointer:
			if out.Field(i).Pointer() == in.Field(i).Pointer() {
				t.Errorf("cloneScope shares access.Scope.%s with its input", in.Type().Field(i).Name)
			}
		}
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
