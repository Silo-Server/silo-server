package metadata

import (
	"context"

	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	cachedPhotoKey = "local/people/1/abcd1234/profile/original.webp"
	tmdbPhotoURL   = "https://image.tmdb.org/t/p/original/remote.jpg"
)

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
