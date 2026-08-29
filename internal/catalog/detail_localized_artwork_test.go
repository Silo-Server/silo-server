package catalog

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/models"
)

// recordingLocalizedArtworkResolver captures the artwork targets a detail build
// mints so a test can assert which surface owns each slot.
type recordingLocalizedArtworkResolver struct {
	targetsBySlot map[string]artworkurl.Target
}

func newRecordingLocalizedArtworkResolver() *recordingLocalizedArtworkResolver {
	return &recordingLocalizedArtworkResolver{targetsBySlot: map[string]artworkurl.Target{}}
}

func (*recordingLocalizedArtworkResolver) ResolveImageURL(context.Context, string, string) string {
	return ""
}

func (*recordingLocalizedArtworkResolver) ResolveImageURLs(context.Context, []string, string) map[string]string {
	return nil
}

func (r *recordingLocalizedArtworkResolver) ResolveArtworkTargetsWithExpiry(
	_ context.Context,
	targets []artworkurl.Target,
	_ string,
) map[string]ResolvedImageURL {
	resolved := make(map[string]ResolvedImageURL, len(targets))
	for _, target := range targets {
		r.targetsBySlot[target.Slot] = target
		resolved[target.CacheKey()] = ResolvedImageURL{URL: "resolved:" + target.Surface}
	}
	return resolved
}

func (r *recordingLocalizedArtworkResolver) ResolveArtworkTargetRequestsWithExpiry(
	ctx context.Context,
	requests []artworkurl.TargetRequest,
) map[string]ResolvedImageURL {
	result := make(map[string]ResolvedImageURL, len(requests))
	for _, request := range requests {
		resolved := r.ResolveArtworkTargetsWithExpiry(ctx, []artworkurl.Target{request.Target}, request.Variant)
		result[request.CacheKey()] = resolved[request.Target.CacheKey()]
	}
	return result
}

// A localization row only fills the fields the provider actually translated.
// When it overrides the poster alone, its backdrop_path and logo_path stay
// empty, so pointing those capabilities at the localization surface would
// dead-end on an empty selection and render a placeholder even though the base
// row carries a perfectly good backdrop and logo. Each slot must therefore
// choose its own surface, exactly as the list path does.
func TestBuildMediaItemDetail_LocalizesOnlyOverriddenArtworkSlots(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name              string
		localizedPoster   string
		localizedBackdrop string
		localizedLogo     string
		wantPoster        string
		wantBackdrop      string
		wantLogo          string
	}{
		{
			name:              "poster only",
			localizedPoster:   "items/movie-1/fr/poster.jpg",
			localizedBackdrop: "items/movie-1/backdrop.jpg",
			localizedLogo:     "items/movie-1/logo.png",
			wantPoster:        artworkurl.SurfaceLocalizedItemPosters,
			wantBackdrop:      artworkurl.SurfaceItemBackdrops,
			wantLogo:          artworkurl.SurfaceItemLogos,
		},
		{
			name:              "backdrop and logo only",
			localizedPoster:   "items/movie-1/poster.jpg",
			localizedBackdrop: "items/movie-1/fr/backdrop.jpg",
			localizedLogo:     "items/movie-1/fr/logo.png",
			wantPoster:        artworkurl.SurfaceItemPosters,
			wantBackdrop:      artworkurl.SurfaceLocalizedItemBackdrops,
			wantLogo:          artworkurl.SurfaceLocalizedItemLogos,
		},
		{
			name:              "every slot localized",
			localizedPoster:   "items/movie-1/fr/poster.jpg",
			localizedBackdrop: "items/movie-1/fr/backdrop.jpg",
			localizedLogo:     "items/movie-1/fr/logo.png",
			wantPoster:        artworkurl.SurfaceLocalizedItemPosters,
			wantBackdrop:      artworkurl.SurfaceLocalizedItemBackdrops,
			wantLogo:          artworkurl.SurfaceLocalizedItemLogos,
		},
		{
			name:              "no slot localized",
			localizedPoster:   "items/movie-1/poster.jpg",
			localizedBackdrop: "items/movie-1/backdrop.jpg",
			localizedLogo:     "items/movie-1/logo.png",
			wantPoster:        artworkurl.SurfaceItemPosters,
			wantBackdrop:      artworkurl.SurfaceItemBackdrops,
			wantLogo:          artworkurl.SurfaceItemLogos,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			base := &models.MediaItem{
				ContentID:        "movie-1",
				Type:             "series",
				Title:            "Show",
				PosterPath:       "items/movie-1/poster.jpg",
				BackdropPath:     "items/movie-1/backdrop.jpg",
				LogoPath:         "items/movie-1/logo.png",
				OriginalLanguage: "en",
			}
			localized := *base
			localized.PosterPath = test.localizedPoster
			localized.BackdropPath = test.localizedBackdrop
			localized.LogoPath = test.localizedLogo

			resolver := newRecordingLocalizedArtworkResolver()
			svc := &DetailService{}
			svc.SetImageResolver(resolver)

			detail, err := svc.buildMediaItemDetail(
				context.Background(),
				base,
				base.ContentID,
				AccessFilter{ProfilePreferredLanguage: "fr"},
				&itemDetailPrefetch{
					haveLocalization: true,
					localizedItem:    &localized,
					haveCredits:      true,
					haveVideos:       true,
					haveExtras:       true,
					haveWorkSummary:  true,
				},
			)
			if err != nil {
				t.Fatalf("buildMediaItemDetail: %v", err)
			}

			for _, want := range []struct {
				slot        string
				wantSurface string
				gotURL      string
			}{
				{slot: "poster", wantSurface: test.wantPoster, gotURL: detail.PosterURL},
				{slot: "backdrop", wantSurface: test.wantBackdrop, gotURL: detail.BackdropURL},
				{slot: "logo", wantSurface: test.wantLogo, gotURL: detail.LogoURL},
			} {
				target, ok := resolver.targetsBySlot[want.slot]
				if !ok {
					t.Fatalf("no artwork target minted for slot %q", want.slot)
				}
				if target.Surface != want.wantSurface {
					t.Errorf("%s surface = %q, want %q", want.slot, target.Surface, want.wantSurface)
				}
				wantKeys := 1
				if want.wantSurface != artworkurl.SurfaceItemPosters &&
					want.wantSurface != artworkurl.SurfaceItemBackdrops &&
					want.wantSurface != artworkurl.SurfaceItemLogos {
					wantKeys = 2
				}
				if len(target.Keys) != wantKeys || target.Keys[0] != base.ContentID {
					t.Fatalf("%s keys = %v, want %d key(s) starting with %q",
						want.slot, target.Keys, wantKeys, base.ContentID)
				}
				if wantKeys == 2 && target.Keys[1] != "fr" {
					t.Errorf("%s language key = %q, want fr", want.slot, target.Keys[1])
				}
				if got, expect := want.gotURL, "resolved:"+want.wantSurface; got != expect {
					t.Errorf("%s URL = %q, want %q", want.slot, got, expect)
				}
			}
		})
	}
}
