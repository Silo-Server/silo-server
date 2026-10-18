package catalog

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
)

const testPhotoPath = "tmdb/people/287/profile/original.abc123.webp"

func personCreditPhoto(t *testing.T, size imagesize.Size) (*recordingCatalogImageResolver, PersonCredit) {
	t.Helper()
	resolver := &recordingCatalogImageResolver{}
	detail := &DetailService{}
	detail.SetImageResolver(resolver)

	credits := detail.personCredits(
		context.Background(),
		[]models.ItemPerson{{Person: models.Person{ID: 287, Name: "Brad Pitt", PhotoPath: testPhotoPath}}},
		AccessFilter{ImageSize: size},
	)
	if len(credits) != 1 {
		t.Fatalf("credits = %d, want 1", len(credits))
	}
	return resolver, credits[0]
}

// Without a requested size a cached headshot is the profile ladder's medium
// rung, the w500 the people endpoints serve, never the stored original, which
// can be up to 3840px tall. Plugin-backed photos get the large hint so
// providers such as TMDB serve a headshot sharp enough for a high-density card.
func TestPersonCreditPhotoUnsetIsMediumRung(t *testing.T) {
	resolver, credit := personCreditPhoto(t, imagesize.Unset)

	if want := "tmdb/people/287/profile/w500.abc123.webp"; resolver.path != want {
		t.Errorf("resolved path = %q, want %q", resolver.path, want)
	}
	if resolver.variant != "large" {
		t.Errorf("resolver variant = %q, want large", resolver.variant)
	}
	if credit.PhotoURL == "" {
		t.Error("PhotoURL is empty")
	}
}

// An explicit size resolves the profile ladder, which is {500, 300} — headshots
// have no wide rung, so large lands on the same key as medium.
func TestPersonCreditPhotoHonorsImageSize(t *testing.T) {
	tests := []struct {
		size        imagesize.Size
		wantPath    string
		wantVariant string
	}{
		{imagesize.Small, "tmdb/people/287/profile/w300.abc123.webp", "card"},
		{imagesize.Medium, "tmdb/people/287/profile/w500.abc123.webp", "featured"},
		{imagesize.Large, "tmdb/people/287/profile/w500.abc123.webp", "large"},
		{imagesize.Original, testPhotoPath, "original"},
	}
	for _, tt := range tests {
		t.Run(string(tt.size), func(t *testing.T) {
			resolver, credit := personCreditPhoto(t, tt.size)
			if resolver.path != tt.wantPath {
				t.Errorf("resolved path = %q, want %q", resolver.path, tt.wantPath)
			}
			if resolver.variant != tt.wantVariant {
				t.Errorf("resolver variant = %q, want %q", resolver.variant, tt.wantVariant)
			}
			if credit.PhotoURL == "" {
				t.Error("PhotoURL is empty")
			}
		})
	}
}

// An absent or sentinel photo path stays empty at every size rather than
// resolving a bogus key.
func TestPersonCreditPhotoSkipsMissingPaths(t *testing.T) {
	for _, photoPath := range []string{"", "-"} {
		resolver := &recordingCatalogImageResolver{}
		detail := &DetailService{}
		detail.SetImageResolver(resolver)

		credits := detail.personCredits(
			context.Background(),
			[]models.ItemPerson{{Person: models.Person{ID: 1, Name: "Nobody", PhotoPath: photoPath}}},
			AccessFilter{ImageSize: imagesize.Large},
		)
		if len(credits) != 1 || credits[0].PhotoURL != "" {
			t.Errorf("photo path %q produced %+v, want an empty PhotoURL", photoPath, credits)
		}
		if resolver.path != "" {
			t.Errorf("photo path %q reached the resolver as %q", photoPath, resolver.path)
		}
	}
}

// A photo width overrides image_size for headshots alone.
func TestPersonCreditPhotoFollowsPhotoWidth(t *testing.T) {
	resolver := &recordingCatalogImageResolver{}
	detail := &DetailService{}
	detail.SetImageResolver(resolver)
	detail.personCredits(
		context.Background(),
		[]models.ItemPerson{{Person: models.Person{ID: 287, Name: "Brad Pitt", PhotoPath: testPhotoPath}}},
		AccessFilter{ImageSize: imagesize.Large, ImageWidths: imagesize.Widths{Photo: 200, Backdrop: 2688}},
	)
	if want := "tmdb/people/287/profile/w300.abc123.webp"; resolver.path != want {
		t.Errorf("resolved path = %q, want %q", resolver.path, want)
	}
	// A plugin's card headshot is softer than a cast card, so a width short of
	// the original keeps the large hint.
	if resolver.variant != imagesize.PluginVariantLarge {
		t.Errorf("resolver variant = %q, want %q", resolver.variant, imagesize.PluginVariantLarge)
	}
}
