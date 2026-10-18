package metadata

import (
	"context"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	cachedPhotoKey = "local/people/1/abcd1234/profile/original.webp"
	tmdbPhotoURL   = "https://image.tmdb.org/t/p/original/remote.jpg"
)

func TestLocalPersonPhotoClaims(t *testing.T) {
	t.Parallel()

	local := "file:///media/Movies/Film/.actors/Lead.jpg"
	tests := []struct {
		name      string
		stored    catalog.PersonPhotoState
		wanted    string
		wantClaim bool
		wantQueue bool
	}{
		{name: "no photo", stored: catalog.PersonPhotoState{}, wanted: local, wantClaim: true},
		{name: "no-photo sentinel", stored: catalog.PersonPhotoState{Path: "-"}, wanted: local, wantClaim: true},
		// A provider photo still waiting for its copy is a photo: claiming it
		// would leave the provider URL uncached for good if the NFO's photo
		// can't be copied.
		{name: "uncached provider URL stays", stored: catalog.PersonPhotoState{Path: tmdbPhotoURL, Source: tmdbPhotoURL}, wanted: local},
		{name: "provider URL a credit wrote without a source stays", stored: catalog.PersonPhotoState{Path: tmdbPhotoURL}, wanted: local},
		{name: "another title's NFO photo waiting for its copy stays",
			stored: catalog.PersonPhotoState{Source: "file:///media/Movies/Other/.actors/Lead.jpg"}, wanted: local},
		{name: "cached provider photo stays", stored: catalog.PersonPhotoState{Path: cachedPhotoKey, Source: tmdbPhotoURL}, wanted: local},
		{name: "cached photo from another title's NFO stays",
			stored: catalog.PersonPhotoState{Path: cachedPhotoKey, Source: "file:///media/Movies/Other/.actors/Lead.jpg"}, wanted: local},
		{name: "cached photo from this NFO's folder is replaced",
			stored: catalog.PersonPhotoState{Path: cachedPhotoKey, Source: "file:///media/Movies/Film/.actors/Lead-old.jpg"}, wanted: local, wantClaim: true},
		// Discovery fingerprints a local source with its size and mtime, so a
		// photo replaced under the same name is a new source in the same folder.
		{name: "replaced photo under the same name is claimed again",
			stored: catalog.PersonPhotoState{Path: cachedPhotoKey, Source: "file:///media/Movies/Film/.actors/Lead.jpg?v=10-1"},
			wanted: "file:///media/Movies/Film/.actors/Lead.jpg?v=12-2", wantClaim: true},
		// Queued again: the cached photo may predate the claim, if the claim
		// committed and its job was never enqueued. The job row dedupes a
		// finished copy.
		{name: "already this source and cached", stored: catalog.PersonPhotoState{Path: cachedPhotoKey, Source: local}, wanted: local, wantQueue: true},
		{name: "already this source, not cached yet", stored: catalog.PersonPhotoState{Source: local}, wanted: local, wantQueue: true},
		{name: "remote NFO photo over a cached local one stays",
			stored: catalog.PersonPhotoState{Path: cachedPhotoKey, Source: local}, wanted: "nfo+https://img.example.org/lead.jpg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims, queue := localPersonPhotoClaims([]int64{1}, map[int64]string{1: tt.wanted}, map[int64]catalog.PersonPhotoState{1: tt.stored})
			if gotClaim := len(claims) == 1; gotClaim != tt.wantClaim {
				t.Fatalf("claims = %#v, want claim %v", claims, tt.wantClaim)
			}
			if tt.wantClaim && claims[0] != (catalog.PersonPhotoSourceClaim{ID: 1, Expected: tt.stored, Source: tt.wanted}) {
				t.Errorf("claim = %#v", claims[0])
			}
			if gotQueue := len(queue) == 1; gotQueue != tt.wantQueue {
				t.Errorf("queue = %v, want queued %v", queue, tt.wantQueue)
			}
		})
	}
	// A person who is gone is skipped.
	if claims, queue := localPersonPhotoClaims([]int64{2}, map[int64]string{2: local}, nil); claims != nil || queue != nil {
		t.Errorf("missing person: claims %v, queue %v", claims, queue)
	}
}

type fakeLocalPersonPhotoRepo struct {
	states map[int64]catalog.PersonPhotoState
	claims []catalog.PersonPhotoSourceClaim
	// lose makes the claim for an id lose its race.
	lose map[int64]bool
}

func (r *fakeLocalPersonPhotoRepo) PhotoStates(_ context.Context, ids []int64) (map[int64]catalog.PersonPhotoState, error) {
	out := make(map[int64]catalog.PersonPhotoState)
	for _, id := range ids {
		if state, ok := r.states[id]; ok {
			out[id] = state
		}
	}
	return out, nil
}

func (r *fakeLocalPersonPhotoRepo) ClaimPhotoSources(_ context.Context, claims []catalog.PersonPhotoSourceClaim) ([]int64, error) {
	r.claims = append(r.claims, claims...)
	var claimed []int64
	for _, claim := range claims {
		if !r.lose[claim.ID] {
			claimed = append(claimed, claim.ID)
		}
	}
	return claimed, nil
}

func TestLocalPersonPhotoJobs(t *testing.T) {
	t.Parallel()

	local := "file:///media/Movies/Film/.actors/Lead.jpg"
	remote := "nfo+https://img.example.org/support.jpg"
	repo := &fakeLocalPersonPhotoRepo{
		states: map[int64]catalog.PersonPhotoState{
			1: {Path: "-"},                                  // takes the local photo
			2: {},                                           // takes the remote photo
			3: {Path: cachedPhotoKey, Source: tmdbPhotoURL}, // keeps its cached photo
			4: {},                                           // loses the race
			6: {Path: tmdbPhotoURL, Source: tmdbPhotoURL},   // keeps its provider photo
			// Holds the source with an older cached copy: a claim whose job was
			// never enqueued. Queued again, without forcing a finished copy.
			7: {Path: cachedPhotoKey, Source: "file:///media/Movies/Film/.actors/Seven.jpg"},
		},
		lose: map[int64]bool{4: true},
	}
	people := []models.ItemPerson{
		{Person: models.Person{ID: 1, Name: "Lead"}, LocalPhotoSource: local},
		{Person: models.Person{ID: 1, Name: "Lead"}, Kind: models.PersonKindDirector, LocalPhotoSource: "file:///media/Movies/Film/other.jpg"},
		{Person: models.Person{ID: 2, Name: "Support"}, LocalPhotoSource: remote},
		{Person: models.Person{ID: 3, Name: "Star"}, LocalPhotoSource: "file:///media/Movies/Film/.actors/Star.jpg"},
		{Person: models.Person{ID: 4, Name: "Raced"}, LocalPhotoSource: "file:///media/Movies/Film/.actors/Raced.jpg"},
		{Person: models.Person{ID: 5, Name: "No Photo"}},
		{Person: models.Person{ID: 6, Name: "Provider Photo"}, LocalPhotoSource: "file:///media/Movies/Film/.actors/Provider.jpg"},
		{Person: models.Person{ID: 7, Name: "Seven"}, LocalPhotoSource: "file:///media/Movies/Film/.actors/Seven.jpg"},
		{Person: models.Person{Name: "Unsaved"}, LocalPhotoSource: local},
	}
	jobs := localPersonPhotoJobs(context.Background(), repo, "movie-1", people)

	wantClaims := []catalog.PersonPhotoSourceClaim{
		{ID: 1, Expected: catalog.PersonPhotoState{Path: "-"}, Source: local},
		{ID: 2, Source: remote},
		{ID: 4, Source: "file:///media/Movies/Film/.actors/Raced.jpg"},
	}
	if !reflect.DeepEqual(repo.claims, wantClaims) {
		t.Errorf("claims = %#v\nwant %#v", repo.claims, wantClaims)
	}
	wantJobs := []EnqueueImageCacheJobInput{
		{
			TargetType: ImageCacheTargetPerson, TargetContentID: "1", SeriesID: "movie-1", SourcePath: local,
			ProviderID: "local", ProviderContentID: "1", ContentType: "people", ImageType: ImageCacheImageProfile,
			RequeueSucceeded: true,
		},
		{
			TargetType: ImageCacheTargetPerson, TargetContentID: "2", SeriesID: "movie-1", SourcePath: remote,
			ProviderID: "nfo", ProviderContentID: "2", ContentType: "people", ImageType: ImageCacheImageProfile,
			RequeueSucceeded: true,
		},
		{
			TargetType: ImageCacheTargetPerson, TargetContentID: "7", SeriesID: "movie-1", SourcePath: "file:///media/Movies/Film/.actors/Seven.jpg",
			ProviderID: "local", ProviderContentID: "7", ContentType: "people", ImageType: ImageCacheImageProfile,
		},
	}
	if !reflect.DeepEqual(jobs, wantJobs) {
		t.Errorf("jobs = %#v\nwant %#v", jobs, wantJobs)
	}
}

// Without the image cache queue, local photos are not used, like local
// artwork; the zero repository below would panic if it were read.
func TestApplyLocalPersonPhotosNeedsImageCache(t *testing.T) {
	t.Parallel()

	service := &MetadataService{personRepo: &catalog.PersonRepository{}}
	service.applyLocalPersonPhotos(context.Background(), "movie-1", []models.ItemPerson{
		{Person: models.Person{ID: 1}, LocalPhotoSource: "file:///media/a.jpg"},
	})
}

// With metadata.cache_images off, local photos are not used at all, sidecar
// or NFO-named: clients load provider artwork straight from the providers.
// The zero repository would panic if it were read.
func TestApplyLocalPersonPhotosSkippedWithCachingOff(t *testing.T) {
	t.Parallel()

	service := &MetadataService{personRepo: &catalog.PersonRepository{}, imageCacheJobs: &capturingImageJobEnqueuer{}}
	service.applyLocalPersonPhotos(context.Background(), "movie-1", []models.ItemPerson{
		{Person: models.Person{ID: 1}, LocalPhotoSource: "file:///media/Film/.actors/Lead.jpg"},
		{Person: models.Person{ID: 2}, LocalPhotoSource: "nfo+https://img.example.org/support.jpg"},
	})
}

// The NFO photo a credit names survives the merge with a remote provider's
// credit for the same person.
func TestMergePeopleKeepsLocalPhotoSource(t *testing.T) {
	t.Parallel()

	target := []models.ItemPerson{{Person: models.Person{Name: "Lead"}, Kind: models.PersonKindActor, LocalPhotoSource: "file:///a.jpg"}}
	mergePeople(&target, []models.ItemPerson{{Person: models.Person{Name: "Lead", TmdbID: "5", PhotoPath: tmdbPhotoURL}, Kind: models.PersonKindActor}}, MergeFillEmpty)
	if len(target) != 1 || target[0].LocalPhotoSource != "file:///a.jpg" || target[0].PhotoPath != tmdbPhotoURL || target[0].TmdbID != "5" {
		t.Fatalf("merged = %#v", target)
	}

	stored := []models.ItemPerson{{Person: models.Person{ID: 9, Name: "Lead", PhotoPath: cachedPhotoKey}, Kind: models.PersonKindActor}}
	mergePeople(&stored, []models.ItemPerson{{Person: models.Person{Name: "Lead"}, Kind: models.PersonKindActor, LocalPhotoSource: "file:///a.jpg"}}, MergeFillEmpty)
	if stored[0].LocalPhotoSource != "file:///a.jpg" || stored[0].PhotoPath != cachedPhotoKey {
		t.Fatalf("merged into stored = %#v", stored)
	}
}

// A provider refresh keeps a cached photo a title's NFO named, and fills one
// that never made it into the cache.
func TestPersonRefreshKeepsCachedLocalPhoto(t *testing.T) {
	t.Parallel()

	local := "file:///media/Movies/Film/.actors/Lead.jpg"
	provider := stubPersonProvider{slug: "tmdb", detail: &PersonDetailResult{Name: "Lead", PhotoPath: tmdbPhotoURL}}
	for _, tc := range []struct {
		name       string
		stored     models.Person
		wantPath   string
		wantSource string
	}{
		{
			name:       "cached local photo",
			stored:     models.Person{ID: 1, Name: "Lead", TmdbID: "1", PhotoPath: cachedPhotoKey, PhotoSourcePath: local, PhotoThumbhash: "th"},
			wantPath:   cachedPhotoKey,
			wantSource: local,
		},
		{
			name:       "local photo never cached",
			stored:     models.Person{ID: 1, Name: "Lead", TmdbID: "1", PhotoSourcePath: local},
			wantPath:   tmdbPhotoURL,
			wantSource: tmdbPhotoURL,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakePersonRefreshRepo(tc.stored)
			service := &PersonRefreshService{repo: repo}
			if _, err := service.refreshPersonWithProviders(context.Background(), 1, []Provider{provider}); err != nil {
				t.Fatalf("refresh: %v", err)
			}
			got := repo.persons[1]
			if got.PhotoPath != tc.wantPath || got.PhotoSourcePath != tc.wantSource {
				t.Fatalf("photo = %q from %q, want %q from %q", got.PhotoPath, got.PhotoSourcePath, tc.wantPath, tc.wantSource)
			}
		})
	}
}

// A person credited twice, as actor and director, keeps both credits when a
// provider's credits carry the person's ID: the ID matches the credit of the
// same kind only, so each credit gets the ID.
func TestMergePeopleMatchesIDsWithinACreditKind(t *testing.T) {
	t.Parallel()

	target := []models.ItemPerson{
		{Person: models.Person{Name: "Clint Eastwood", TmdbID: "190"}, Kind: models.PersonKindActor, Character: "Walt"},
		{Person: models.Person{Name: "Clint Eastwood"}, Kind: models.PersonKindDirector},
	}
	mergePeople(&target, []models.ItemPerson{
		{Person: models.Person{Name: "Clint Eastwood", TmdbID: "190"}, Kind: models.PersonKindActor},
		{Person: models.Person{Name: "Clint Eastwood", TmdbID: "190"}, Kind: models.PersonKindDirector},
	}, MergeFillEmpty)
	if len(target) != 2 {
		t.Fatalf("credits = %#v, want the actor and director credits", target)
	}
	for _, credit := range target {
		if credit.TmdbID != "190" {
			t.Errorf("%v credit TmdbID = %q, want 190", credit.Kind, credit.TmdbID)
		}
	}
}

// An actor one provider lists as a guest star is still one cast credit when
// the provider IDs agree.
func TestMergePeopleMatchesIDsAcrossCastKinds(t *testing.T) {
	t.Parallel()

	target := []models.ItemPerson{{Person: models.Person{Name: "Guest", TmdbID: "77"}, Kind: models.PersonKindActor, Character: "Neighbor"}}
	mergePeople(&target, []models.ItemPerson{
		{Person: models.Person{Name: "Guest", TmdbID: "77", PhotoPath: tmdbPhotoURL}, Kind: models.PersonKindGuestStar},
	}, MergeFillEmpty)
	if len(target) != 1 || target[0].PhotoPath != tmdbPhotoURL {
		t.Fatalf("credits = %#v, want one cast credit enriched with the photo", target)
	}

	// An NFO actor without ids matches the provider's guest star by name, so
	// the provider's id joins the credit that carries the NFO's photo.
	target = []models.ItemPerson{{Person: models.Person{Name: "Jane Doe"}, Kind: models.PersonKindActor, LocalPhotoSource: "file:///media/Show/.actors/Jane_Doe.jpg"}}
	mergePeople(&target, []models.ItemPerson{
		{Person: models.Person{Name: "Jane Doe", TmdbID: "88"}, Kind: models.PersonKindGuestStar},
	}, MergeFillEmpty)
	if len(target) != 1 || target[0].TmdbID != "88" || target[0].LocalPhotoSource == "" {
		t.Fatalf("credits = %#v, want one cast credit with the provider's id and the NFO's photo", target)
	}

	// A crew credit with the same name stays its own credit.
	mergePeople(&target, []models.ItemPerson{{Person: models.Person{Name: "Jane Doe"}, Kind: models.PersonKindDirector}}, MergeFillEmpty)
	if len(target) != 2 {
		t.Fatalf("credits = %#v, want the director credit kept apart", target)
	}
}
