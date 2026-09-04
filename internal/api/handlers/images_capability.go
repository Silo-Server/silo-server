package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/imageutil"
)

// imageTypesWithWidths is every artwork type a client can receive a URL for.
// Cast and crew headshots ("profile") are served through the same ladder, so
// they are advertised too.
var imageTypesWithWidths = []string{
	artworkkey.ImagePoster,
	artworkkey.ImageBackdrop,
	artworkkey.ImageStill,
	artworkkey.ImageLogo,
	artworkkey.ImageProfile,
}

// imageSizeWidths reports the pixel width behind each named size for one image
// type. A size is absent from the map only if it resolves to the original,
// whose width is bounded by original_max_width_px rather than fixed.
type imageSizeWidths struct {
	Small  int `json:"small"`
	Medium int `json:"medium"`
	Large  int `json:"large"`
}

// imagesCapabilityResponse describes the client-selectable image size contract.
//
// Per the v1 rules, new functionality is feature-detected rather than inferred
// from a server version. A client that gets a 404 here is talking to a server
// that predates image_size: it should keep using the server's per-context
// defaults instead of sending a parameter that would be ignored.
type imagesCapabilityResponse struct {
	SchemaVersion int `json:"schema_version"`
	// Param is the query parameter name, so a client does not hardcode it.
	Param string `json:"param"`
	// Sizes is every value the parameter accepts, narrowest first. Sending
	// anything else is a 400 rather than a silent fallback.
	Sizes []imagesize.Size `json:"sizes"`
	// Widths gives the pixel width each size resolves to, per image type. It is
	// derived live from the server's variant ladder, so a client sizing its
	// requests from these numbers stays correct across ladder changes.
	Widths map[string]imageSizeWidths `json:"widths"`
	// OriginalMaxWidthPx bounds the "original" size: cached originals are
	// downscaled to this on ingest, so asking for original never yields more.
	OriginalMaxWidthPx int `json:"original_max_width_px"`
	// TextlessPoster is present only when the viewer-facing endpoint is wired
	// in this deployment. Clients feature-detect it instead of guessing from a
	// server version.
	TextlessPoster *textlessPosterCapability `json:"textless_poster,omitempty"`
}

type textlessPosterCapability struct {
	Endpoint       string   `json:"endpoint"`
	SupportedTypes []string `json:"supported_types"`
}

// HandleImagesCapability reports the image_size contract.
func HandleImagesCapability(w http.ResponseWriter, r *http.Request) {
	handleImagesCapability(w, false)
}

// NewImagesCapabilityHandler returns a capability handler whose optional
// fields reflect the routes actually wired by this deployment.
func NewImagesCapabilityHandler(textlessPosterEnabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		handleImagesCapability(w, textlessPosterEnabled)
	}
}

func handleImagesCapability(w http.ResponseWriter, textlessPosterEnabled bool) {
	widths := make(map[string]imageSizeWidths, len(imageTypesWithWidths))
	for _, imageType := range imageTypesWithWidths {
		widths[imageType] = imageSizeWidths{
			Small:  variantWidthPx(imageType, imagesize.Small),
			Medium: variantWidthPx(imageType, imagesize.Medium),
			Large:  variantWidthPx(imageType, imagesize.Large),
		}
	}

	response := imagesCapabilityResponse{
		SchemaVersion:      1,
		Param:              imagesize.QueryParam,
		Sizes:              imagesize.All,
		Widths:             widths,
		OriginalMaxWidthPx: imageutil.MaxCachedOriginalDimension,
	}
	if textlessPosterEnabled {
		response.TextlessPoster = &textlessPosterCapability{
			Endpoint:       "/api/v1/catalog/items/{id}/images/textless-poster",
			SupportedTypes: []string{textlessPosterMovieType, textlessPosterSeriesType},
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// variantWidthPx reports the pixel width a size resolves to for an image type.
// A size that resolves to the original has no fixed width and reports 0, which
// original_max_width_px covers instead.
func variantWidthPx(imageType string, size imagesize.Size) int {
	width, _ := imagesize.VariantWidthPx(imagesize.Variant(imageType, size))
	return width
}
