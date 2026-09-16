package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/imageutil"
)

const (
	artworkDeliveryServer = "server"
	artworkDeliveryDirect = "direct"
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

// ImageSizeWidths reports the pixel width behind each named size for one image
// type. A size is absent from the map only if it resolves to the original,
// whose width is bounded by original_max_width_px rather than fixed.
type ImageSizeWidths struct {
	Small  int `json:"small"`
	Medium int `json:"medium"`
	Large  int `json:"large"`
}

// ImagesCapabilityResponse describes the client-selectable image size contract.
//
// Per the v1 rules, new functionality is feature-detected rather than inferred
// from a server version. A client that gets a 404 here is talking to a server
// that predates image_size: it should keep using the server's per-context
// defaults instead of sending a parameter that would be ignored.
type ImagesCapabilityResponse struct {
	SchemaVersion int `json:"schema_version"`
	// SeasonListArtworkParam names the series-seasons query parameter a client
	// sends as false to receive text-only season rows without poster preparation.
	SeasonListArtworkParam string `json:"season_list_artwork_param"`
	// Param is the query parameter name, so a client does not hardcode it.
	Param string `json:"param"`
	// Sizes is every value the parameter accepts, narrowest first. Sending
	// anything else is a 400 rather than a silent fallback.
	Sizes []imagesize.Size `json:"sizes"`
	// Widths gives the pixel width each size resolves to, per image type. It is
	// derived live from the server's variant ladder, so a client sizing its
	// requests from these numbers stays correct across ladder changes.
	Widths map[string]ImageSizeWidths `json:"widths"`
	// OriginalMaxWidthPx bounds the "original" size: cached originals are
	// downscaled to this on ingest, so asking for original never yields more.
	OriginalMaxWidthPx int    `json:"original_max_width_px"`
	StorageBackend     string `json:"-"`
	Delivery           string `json:"-"`
}

// HandleImagesCapability reports the image_size contract.
func HandleImagesCapability(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(GetImagesCapability())
}

// GetImagesCapability derives the shared size discovery view from the actual ladder.
func GetImagesCapability(backend ...string) ImagesCapabilityResponse {
	storageBackend, delivery := artworkstore.BackendLocal, artworkDeliveryServer
	if len(backend) > 0 && backend[0] == "s3" {
		storageBackend, delivery = artworkstore.BackendS3, artworkDeliveryDirect
	}
	widths := make(map[string]ImageSizeWidths, len(imageTypesWithWidths))
	for _, imageType := range imageTypesWithWidths {
		widths[imageType] = ImageSizeWidths{
			Small:  variantWidthPx(imageType, imagesize.Small),
			Medium: variantWidthPx(imageType, imagesize.Medium),
			Large:  variantWidthPx(imageType, imagesize.Large),
		}
	}

	return ImagesCapabilityResponse{
		SchemaVersion:          1,
		SeasonListArtworkParam: seasonListArtworkParam,
		Param:                  imagesize.QueryParam,
		Sizes:                  imagesize.All,
		Widths:                 widths,
		OriginalMaxWidthPx:     imageutil.MaxCachedOriginalDimension,
		StorageBackend:         storageBackend,
		Delivery:               delivery,
	}
}

// variantWidthPx reports the pixel width a size resolves to for an image type.
// A size that resolves to the original has no fixed width and reports 0, which
// original_max_width_px covers instead.
func variantWidthPx(imageType string, size imagesize.Size) int {
	width, _ := imagesize.VariantWidthPx(imagesize.Variant(imageType, size))
	return width
}
