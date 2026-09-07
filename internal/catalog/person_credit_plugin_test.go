package catalog_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

type creditPhotoPlugin struct {
	calls, paths int
	expiresAt    *time.Time
}

func (p *creditPhotoPlugin) ResolveImageURL(ctx context.Context, path, variant string) (string, error) {
	result, err := p.ResolveImageURLWithExpiry(ctx, path, variant)
	return result.URL, err
}

func (p *creditPhotoPlugin) ResolveImageURLs(ctx context.Context, paths []string, variant string) (map[string]string, error) {
	values, err := p.ResolveImageURLsWithExpiry(ctx, paths, variant)
	result := make(map[string]string, len(values))
	for path, value := range values {
		result[path] = value.URL
	}
	return result, err
}

func (p *creditPhotoPlugin) ResolveImageURLWithExpiry(ctx context.Context, path, variant string) (catalog.ResolvedImageURL, error) {
	values, err := p.ResolveImageURLsWithExpiry(ctx, []string{path}, variant)
	return values[path], err
}

func (p *creditPhotoPlugin) ResolveImageURLsWithExpiry(_ context.Context, paths []string, variant string) (map[string]catalog.ResolvedImageURL, error) {
	p.calls++
	p.paths += len(paths)
	result := make(map[string]catalog.ResolvedImageURL, len(paths))
	for _, path := range paths {
		result[path] = catalog.ResolvedImageURL{URL: "https://example.invalid/" + variant + "/" + path, ExpiresAt: p.expiresAt}
	}
	return result, nil
}

func pluginCreditPeople(count int, duplicates bool) []models.ItemPerson {
	people := make([]models.ItemPerson, count)
	for i := range people {
		id := i
		if duplicates {
			id /= 2
		}
		people[i].PhotoPath = fmt.Sprintf("photos://person-%d.jpg", id)
	}
	return people
}

func TestPersonCreditPhotosThroughPluginResolver(t *testing.T) {
	for _, size := range []imagesize.Size{imagesize.Unset, imagesize.Medium} {
		t.Run(string(size), func(t *testing.T) {
			resolver := metadata.NewPluginImageResolver()
			t.Cleanup(resolver.Close)
			source := &creditPhotoPlugin{expiresAt: new(time.Now().Add(time.Hour))}
			other := &creditPhotoPlugin{expiresAt: source.expiresAt}
			resolver.RegisterSource("photos", source)
			resolver.RegisterSource("other", other)
			service := &catalog.DetailService{}
			service.SetImageResolver(resolver)
			people := pluginCreditPeople(100, true)
			people[99].PhotoPath = "other://different.jpg"
			for _, warm := range []bool{false, true} {
				source.calls, source.paths, other.calls, other.paths = 0, 0, 0, 0
				got := catalog.PersonCreditsForTesting(service, t.Context(), people, catalog.AccessFilter{ImageSize: size})
				if len(got) != len(people) {
					t.Fatalf("got %d credits", len(got))
				}
				for i, credit := range got {
					path := fmt.Sprintf("person-%d.jpg", i/2)
					if i == 99 {
						path = "different.jpg"
					}
					if want := "https://example.invalid/featured/" + path; credit.PhotoURL != want {
						t.Fatalf("credit %d URL=%q, want %q", i, credit.PhotoURL, want)
					}
				}
				wantCalls, wantPaths := 1, 50
				if warm {
					wantCalls, wantPaths = 0, 0
				}
				if source.calls != wantCalls || source.paths != wantPaths || other.calls != wantCalls || other.paths != wantCalls {
					t.Fatalf("warm=%t primary=%d calls/%d paths other=%d calls/%d paths", warm, source.calls, source.paths, other.calls, other.paths)
				}
			}
		})
	}
}

func BenchmarkPersonCreditPhotosPluginResolver(b *testing.B) {
	for _, count := range []int{10, 100, 500} {
		for _, warm := range []bool{false, true} {
			for _, duplicate := range []bool{false, true} {
				b.Run(fmt.Sprintf("credits=%d/warm=%t/duplicates=%t", count, warm, duplicate), func(b *testing.B) {
					resolver := metadata.NewPluginImageResolver()
					b.Cleanup(resolver.Close)
					source := &creditPhotoPlugin{expiresAt: new(time.Now().Add(time.Hour))}
					registrations := []metadata.PluginImageResolverSourceRegistration{{Scheme: "photos", Source: source}}
					resolver.ReplaceSources(registrations)
					service := &catalog.DetailService{}
					service.SetImageResolver(resolver)
					people := pluginCreditPeople(count, duplicate)
					filter := catalog.AccessFilter{}
					if warm {
						catalog.PersonCreditsForTesting(service, b.Context(), people, filter)
					}
					source.calls, source.paths = 0, 0
					b.ReportAllocs()
					for b.Loop() {
						if !warm {
							// Exclude cache invalidation: time only credit conversion and real
							// resolver work with cold URL entries, using a zero-latency provider.
							b.StopTimer()
							resolver.ReplaceSources(registrations)
							b.StartTimer()
						}
						catalog.PersonCreditsForTesting(service, b.Context(), people, filter)
					}
					b.ReportMetric(float64(source.calls)/float64(b.N), "provider-calls/op")
					b.ReportMetric(float64(source.paths)/float64(b.N), "provider-paths/op")
				})
			}
		}
	}
}
