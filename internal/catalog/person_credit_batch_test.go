package catalog

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
)

type creditPhotoResolver struct {
	batches, singles int
	paths            []string
	missingBatch     bool
	emptyBatch       bool
}

func (r *creditPhotoResolver) ResolveImageURL(_ context.Context, path, variant string) string {
	r.singles++
	if strings.Contains(path, "unavailable") {
		return ""
	}
	return "resolved:" + variant + ":" + path
}

func (r *creditPhotoResolver) ResolveImageURLs(_ context.Context, paths []string, variant string) map[string]string {
	r.batches++
	r.paths = append(r.paths, paths...)
	result := make(map[string]string, len(paths))
	for _, path := range paths {
		if r.missingBatch {
			continue
		}
		if r.emptyBatch || strings.Contains(path, "unavailable") {
			result[path] = ""
			continue
		}
		result[path] = "resolved:" + variant + ":" + path
	}
	return result
}

type expiringCreditPhotoResolver struct{ creditPhotoResolver }

func (*expiringCreditPhotoResolver) ResolveImageURL(context.Context, string, string) string {
	panic("must use expiry-aware single resolution")
}

func (*expiringCreditPhotoResolver) ResolveImageURLs(context.Context, []string, string) map[string]string {
	panic("must use expiry-aware batch resolution")
}

func (r *expiringCreditPhotoResolver) ResolveImageURLWithExpiry(ctx context.Context, path, variant string) ResolvedImageURL {
	return ResolvedImageURL{URL: r.creditPhotoResolver.ResolveImageURL(ctx, path, variant), ExpiresAt: new(time.Now().Add(time.Hour))}
}

func (r *expiringCreditPhotoResolver) ResolveImageURLsWithExpiry(ctx context.Context, paths []string, variant string) map[string]ResolvedImageURL {
	urls := r.creditPhotoResolver.ResolveImageURLs(ctx, paths, variant)
	result := make(map[string]ResolvedImageURL, len(urls))
	for path, url := range urls {
		result[path] = ResolvedImageURL{URL: url, ExpiresAt: new(time.Now().Add(time.Hour))}
	}
	return result
}

func creditPhotoPeople() []models.ItemPerson {
	paths := []string{testPhotoPath, "plug://people/second.jpg", testPhotoPath, "https://example.invalid/person.jpg", "http://example.invalid/other.jpg", "", "-", "other://unavailable.jpg"}
	people := make([]models.ItemPerson, len(paths))
	for i, path := range paths {
		people[i] = models.ItemPerson{Person: models.Person{ID: int64(i + 1), Name: fmt.Sprintf("Person %d", i), PhotoPath: path, PhotoThumbhash: "hash", TmdbID: "tmdb", ImdbID: "imdb", TvdbID: "tvdb", PlexGUID: "plex"}, Kind: models.PersonKindActor, Character: "Character", SortOrder: i}
	}
	people[2].Kind = models.PersonKindDirector
	people[5].PhotoThumbhash = ""
	people[6].PhotoThumbhash = "-"
	return people
}

func TestPersonCreditPhotoBatchPreservesCredits(t *testing.T) {
	for _, size := range []imagesize.Size{imagesize.Unset, imagesize.Small, imagesize.Medium, imagesize.Large, imagesize.Original} {
		for _, expiring := range []bool{false, true} {
			for _, mode := range []string{"complete", "missing", "empty"} {
				t.Run(fmt.Sprintf("size=%s/expiring=%t/batch=%s", size, expiring, mode), func(t *testing.T) {
					recorder := &creditPhotoResolver{missingBatch: mode == "missing", emptyBatch: mode == "empty"}
					var resolver ImageResolver = recorder
					if expiring {
						extended := &expiringCreditPhotoResolver{creditPhotoResolver: *recorder}
						recorder = &extended.creditPhotoResolver
						resolver = extended
					}
					service := &DetailService{imageResolver: resolver}
					people := creditPhotoPeople()
					original := slices.Clone(people)
					got := service.personCredits(t.Context(), people, AccessFilter{ImageSize: size})
					if !reflect.DeepEqual(people, original) {
						t.Fatal("mutated input credits")
					}
					// The old individual helpers are the behavior oracle for URL normalization
					// and fallbacks; all other credit fields must preserve source ordering.
					reference := &DetailService{imageResolver: &creditPhotoResolver{}}
					want := make([]PersonCredit, len(people))
					for i, person := range people {
						photo := reference.PresignURL(t.Context(), person.PhotoPath, imagesize.PluginVariantFeatured)
						if size != imagesize.Unset {
							photo = reference.PresignImageURL(t.Context(), person.PhotoPath, "profile", string(size))
						}
						hash := person.PhotoThumbhash
						if hash == "-" {
							hash = ""
						}
						want[i] = PersonCredit{PersonID: person.ID, Name: person.Name, Kind: person.Kind, Character: person.Character, SortOrder: person.SortOrder, TmdbID: person.TmdbID, ImdbID: person.ImdbID, TvdbID: person.TvdbID, PlexGUID: person.PlexGUID, PhotoURL: photo, PhotoThumbhash: hash}
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("credits = %#v, want %#v", got, want)
					}
					wantSingles := 1 // The unavailable photo gets one fallback, then stays empty.
					if mode != "complete" {
						wantSingles = 3
					}
					if recorder.batches != 1 || recorder.singles != wantSingles || len(recorder.paths) != 3 {
						t.Fatalf("batch=%d singles=%d paths=%v, want one batch of three distinct paths and %d fallbacks", recorder.batches, recorder.singles, recorder.paths, wantSingles)
					}
					cast, crew := splitCastCrew(got)
					wantCast, wantCrew := splitCastCrew(want)
					if !reflect.DeepEqual(cast, wantCast) || !reflect.DeepEqual(crew, wantCrew) {
						t.Fatal("cast/crew presentation changed")
					}
				})
			}
		}
	}
}

func TestPersonCreditPhotoBatchSkipsDirectAndMissingPaths(t *testing.T) {
	for _, size := range []imagesize.Size{imagesize.Unset, imagesize.Large} {
		recorder := &creditPhotoResolver{}
		service := &DetailService{imageResolver: recorder}
		people := creditPhotoPeople()[3:7]
		got := service.personCredits(t.Context(), people, AccessFilter{ImageSize: size})
		if recorder.batches != 0 || recorder.singles != 0 {
			t.Fatal("direct or missing paths reached the resolver")
		}
		for i, credit := range got {
			want := people[i].PhotoPath
			if want == "-" {
				want = ""
			}
			if credit.PhotoURL != want {
				t.Fatalf("photo %d = %q, want %q", i, credit.PhotoURL, want)
			}
		}
		if got := service.personCredits(t.Context(), nil, AccessFilter{ImageSize: size}); got == nil || len(got) != 0 {
			t.Fatalf("empty credits = %#v, want non-nil empty slice", got)
		}
	}
}

func TestPersonCreditPhotoBatchWithoutResolver(t *testing.T) {
	service := &DetailService{}
	for _, size := range []imagesize.Size{imagesize.Unset, imagesize.Large} {
		got := service.personCredits(t.Context(), creditPhotoPeople(), AccessFilter{ImageSize: size})
		for i, credit := range got {
			if i == 3 || i == 4 {
				continue
			}
			if credit.PhotoURL != "" {
				t.Fatalf("unresolved photo = %q", credit.PhotoURL)
			}
		}
	}
}

func TestPersonCreditPhotoBatchCoalescesNormalizedKeys(t *testing.T) {
	recorder := &creditPhotoResolver{missingBatch: true}
	service := &DetailService{imageResolver: recorder}
	people := []models.ItemPerson{
		{Person: models.Person{PhotoPath: testPhotoPath}},
		{Person: models.Person{PhotoPath: "tmdb/people/287/profile/w500.abc123.webp"}},
	}
	got := service.personCredits(t.Context(), people, AccessFilter{ImageSize: imagesize.Large})
	if got[0].PhotoURL != got[1].PhotoURL || got[0].PhotoURL == "" {
		t.Fatalf("equivalent stored rungs produced different URLs: %#v", got)
	}
	if recorder.batches != 1 || len(recorder.paths) != 1 || recorder.singles != 1 {
		t.Fatalf("normalized aliases resolved repeatedly: %#v", recorder)
	}
}

func BenchmarkPersonCreditPhotos(b *testing.B) {
	for _, count := range []int{10, 100, 500} {
		for _, duplicate := range []bool{false, true} {
			for _, size := range []imagesize.Size{imagesize.Unset, imagesize.Medium} {
				b.Run(fmt.Sprintf("credits=%d/duplicates=%t/size=%s", count, duplicate, size), func(b *testing.B) {
					recorder := &creditPhotoResolver{}
					service := &DetailService{imageResolver: recorder}
					people := make([]models.ItemPerson, count)
					for i := range people {
						id := i
						if duplicate {
							id /= 2
						}
						people[i].PhotoPath = fmt.Sprintf("plug://people/%d.jpg", id)
					}
					b.ReportAllocs()
					for b.Loop() {
						recorder.paths = recorder.paths[:0]
						service.personCredits(b.Context(), people, AccessFilter{ImageSize: size})
					}
					b.ReportMetric(float64(recorder.batches)/float64(b.N), "batch-calls/op")
					b.ReportMetric(float64(recorder.singles)/float64(b.N), "single-calls/op")
				})
			}
		}
	}
}

// PersonCreditsForTesting lets external integration tests use the production
// PluginImageResolver without creating a catalog -> metadata import cycle.
var PersonCreditsForTesting = (*DetailService).personCredits
