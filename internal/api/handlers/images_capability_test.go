package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/imageutil"
)

func TestHandleImagesCapability(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleImagesCapability(rec, httptest.NewRequest(http.MethodGet, "/api/v1/images/capability", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var got imagesCapabilityResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if got.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", got.SchemaVersion)
	}
	if got.Param != "image_size" {
		t.Errorf("param = %q, want image_size", got.Param)
	}
	wantSizes := []imagesize.Size{imagesize.Small, imagesize.Medium, imagesize.Large, imagesize.Original}
	if len(got.Sizes) != len(wantSizes) {
		t.Fatalf("sizes = %v, want %v", got.Sizes, wantSizes)
	}
	for i, want := range wantSizes {
		if got.Sizes[i] != want {
			t.Errorf("sizes[%d] = %q, want %q", i, got.Sizes[i], want)
		}
	}
	if got.OriginalMaxWidthPx != imageutil.MaxCachedOriginalDimension {
		t.Errorf("original_max_width_px = %d, want %d", got.OriginalMaxWidthPx, imageutil.MaxCachedOriginalDimension)
	}

	// Every advertised width must be a real rung on this server's ladder, so a
	// ladder change can never leave the capability response lying.
	for _, imageType := range imageTypesWithWidths {
		widths, ok := got.Widths[imageType]
		if !ok {
			t.Fatalf("widths missing image type %q", imageType)
		}
		ladder := map[int]bool{}
		for _, width := range artworkkey.VariantWidths(imageType) {
			ladder[width] = true
		}
		for name, width := range map[string]int{"small": widths.Small, "medium": widths.Medium, "large": widths.Large} {
			if !ladder[width] {
				t.Errorf("%s %s width = %d, not a rung in %v", imageType, name, width, artworkkey.VariantWidths(imageType))
			}
		}
		if widths.Small > widths.Medium || widths.Medium > widths.Large {
			t.Errorf("%s widths are not ordered: %+v", imageType, widths)
		}
	}

	if got.Widths["poster"].Large != 780 {
		t.Errorf("poster large width = %d, want 780", got.Widths["poster"].Large)
	}
	if got.Widths["logo"].Large != 1280 {
		t.Errorf("logo large width = %d, want 1280", got.Widths["logo"].Large)
	}
}
