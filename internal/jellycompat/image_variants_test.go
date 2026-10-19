package jellycompat

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

func TestCompatRequestImageSize(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		imageType string
		want      string
	}{
		{"no request defaults to card", "", "Primary", "small"},
		{"no request backdrop defaults to medium", "", "Backdrop", "medium"},
		{"no dimensions", "/Items/1/Images/Primary", "Primary", "small"},
		{"no dimensions backdrop", "/Items/1/Images/Backdrop", "Backdrop", "medium"},
		{"thumbnail width", "/Items/1/Images/Primary?MaxWidth=200", "Primary", "small"},
		{"card boundary", "/Items/1/Images/Primary?FillWidth=320", "Primary", "small"},
		// A rung up to a fifth narrower than the request still serves it.
		{"small within tolerance", "/Items/1/Images/Primary?FillWidth=375", "Primary", "small"},
		{"past small", "/Items/1/Images/Primary?FillWidth=376", "Primary", "medium"},
		{"between small and large", "/Items/1/Images/Primary?MaxWidth=600", "Primary", "medium"},
		{"medium within tolerance", "/Items/1/Images/Primary?MaxWidth=625", "Primary", "medium"},
		{"past medium", "/Items/1/Images/Primary?MaxWidth=626", "Primary", "large"},
		{"lowercase alias", "/Items/1/Images/Primary?maxWidth=900", "Primary", "large"},
		{"fill width reaches large", "/Items/1/Images/Primary?FillWidth=900", "Primary", "large"},
		// A height becomes a width through the type's aspect ratio: a 900px
		// tall poster is 600px wide, which the w500 rung serves.
		{"poster height through aspect", "/Items/1/Images/Primary?MaxHeight=900", "Primary", "medium"},
		{"poster fill height through aspect", "/Items/1/Images/Primary?FillHeight=900", "Primary", "medium"},
		{"tall poster reaches large, not original", "/Items/1/Images/Primary?MaxHeight=1200", "Primary", "large"},
		{"large within tolerance", "/Items/1/Images/Primary?MaxWidth=1250", "Primary", "large"},
		{"past the widest poster rung", "/Items/1/Images/Primary?MaxWidth=1251", "Primary", "original"},
		{"backdrop height through aspect", "/Items/1/Images/Backdrop?FillHeight=1100", "Backdrop", "medium"},
		{"mid-width backdrop stays medium", "/Items/1/Images/Backdrop?FillWidth=1100", "Backdrop", "medium"},
		{"1080p backdrop box", "/Items/1/Images/Backdrop?MaxWidth=1920&MaxHeight=1080", "Backdrop", "medium"},
		// Max bounds both ways, so a short box caps a wide request.
		{"short box caps a wide backdrop", "/Items/1/Images/Backdrop?MaxWidth=3840&MaxHeight=720", "Backdrop", "medium"},
		{"height binds beside a narrow width", "/Items/1/Images/Backdrop?FillWidth=300&FillHeight=1080", "Backdrop", "medium"},
		{"1440p backdrop", "/Items/1/Images/Backdrop?MaxWidth=2560", "Backdrop", "large"},
		{"4K backdrop", "/Items/1/Images/Backdrop?MaxWidth=3840", "Backdrop", "original"},
		{"wide logo", "/Items/1/Images/Logo?MaxWidth=1000", "Logo", "large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			if tt.query != "" {
				req = httptest.NewRequest(http.MethodGet, tt.query, nil)
			}
			// These cases pin the size on the requested type's own ladder.
			if got := compatSizeFor(compatRequestImageSize(req, tt.imageType), compatLadderType(tt.imageType)); got != tt.want {
				t.Fatalf("compatRequestImageSize(%q, %q) = %q, want %q", tt.query, tt.imageType, got, tt.want)
			}
		})
	}
}

// Which image serves a request is known only once the item resolves: Primary
// is a poster, an episode still or a headshot, and an item without the
// requested type falls back to another. Each resolves on its own ladder and
// aspect ratio, so one request can need a different size for each.
func TestCompatRequestImageSizePerLadder(t *testing.T) {
	tests := []struct {
		name      string
		imageType string
		query     string
		want      map[string]string
	}{
		// A headshot's widest rung is 500px, so a 1200px request needs the
		// original, where a poster's w1000 serves it.
		{"wide headshot", "Primary", "?MaxWidth=1200", map[string]string{"poster": "large", "still": "large", "profile": "original", "backdrop": "medium"}},
		// A 1200px-tall 16:9 still is 2134px wide, past its w1280 rung.
		{"tall still", "Primary", "?MaxHeight=1200", map[string]string{"poster": "large", "still": "original", "profile": "original", "backdrop": "medium"}},
		{"card", "Primary", "?MaxWidth=200", map[string]string{"poster": "small", "still": "small", "profile": "small", "backdrop": "small"}},
		{"no dimensions", "Primary", "", map[string]string{"poster": "small", "still": "small", "profile": "small", "backdrop": "small"}},
		// A portrait card box bounds a 16:9 image by its width, but one
		// covering it must be as wide as the box is tall at 16:9.
		{"bounded by a portrait box", "Primary", "?MaxWidth=298&MaxHeight=446", map[string]string{"poster": "small", "still": "small", "profile": "small", "backdrop": "small"}},
		{"covering a portrait box", "Primary", "?FillWidth=298&FillHeight=446", map[string]string{"poster": "small", "still": "large", "profile": "small", "backdrop": "medium"}},
		// A backdrop request served by the item's poster sizes it as a poster.
		{"backdrop falling back to a poster", "Backdrop", "?MaxWidth=1200", map[string]string{"backdrop": "medium", "poster": "large", "logo": "large"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/Items/1/Images/"+tt.imageType+tt.query, nil)
			size := compatRequestImageSize(req, tt.imageType)
			for ladder, want := range tt.want {
				if got := compatSizeFor(size, ladder); got != want {
					t.Errorf("%s: compatSizeFor(%q, %q) = %q, want %q", tt.query, size, ladder, got, want)
				}
			}
		})
	}
}

// The size is picked on the ladder of the key being presigned, whatever type
// the request named.
func TestCompatPresignSizesOnTheKeysOwnLadder(t *testing.T) {
	resolver := &recordingImageResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)
	req := httptest.NewRequest(http.MethodGet, "/Items/1/Images/Backdrop?MaxWidth=1200", nil)
	size := compatRequestImageSize(req, "Backdrop")

	compatPresignImageWithExpiry(detailSvc, t.Context(), "tmdb/movies/1/poster/original.r1.webp", "poster", size)
	if want := "tmdb/movies/1/poster/w1000.r1.webp"; resolver.path != want {
		t.Errorf("fallback poster = %q, want %q", resolver.path, want)
	}
	compatPresignImageWithExpiry(detailSvc, t.Context(), "tmdb/movies/1/backdrop/original.r1.webp", "backdrop", size)
	if want := "tmdb/movies/1/backdrop/w1920.r1.webp"; resolver.path != want {
		t.Errorf("backdrop = %q, want %q", resolver.path, want)
	}
}
