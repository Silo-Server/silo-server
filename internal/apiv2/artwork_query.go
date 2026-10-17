package apiv2

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/imagesize"
)

// ArtworkQuery is how a client sizes a response's artwork: one image_size for
// every image, and per-slot target widths that override it for their slot.
// A slot is named for the response field, so backdrop_width governs an
// episode's backdrop_url even when that holds the episode's still. Embed it in
// an operation's input to accept both; see docs/images-api.md.
type ArtworkQuery struct {
	ImageSize     string `query:"image_size" enum:"small,medium,large,original" doc:"Artwork size for every image in the response; absent picks each surface's default" example:"medium"`
	PosterWidth   int    `query:"poster_width" minimum:"1" maximum:"7680" doc:"Width in device pixels the client draws poster_url at. Overrides image_size for posters: the narrowest cached size at least this wide, else the original" example:"352"`
	BackdropWidth int    `query:"backdrop_width" minimum:"1" maximum:"7680" doc:"Width in device pixels the client draws backdrop_url and backdrop_urls at. Overrides image_size for backdrops" example:"2688"`
	LogoWidth     int    `query:"logo_width" minimum:"1" maximum:"7680" doc:"Width in device pixels the client draws logo_url at. Overrides image_size for logos" example:"1760"`
	StillWidth    int    `query:"still_width" minimum:"1" maximum:"7680" doc:"Width in device pixels the client draws an episode's still_url at. Overrides image_size for episode stills" example:"720"`
	PhotoWidth    int    `query:"photo_width" minimum:"1" maximum:"7680" doc:"Width in device pixels the client draws a cast or crew photo_url at. Overrides image_size for headshots" example:"400"`
}

// Artwork is the request's artwork preference. image_size is enum-validated
// before a handler runs, so a value that fails to parse here is absent.
func (q ArtworkQuery) Artwork() imagesize.Request {
	size, err := imagesize.Parse(q.ImageSize)
	if err != nil {
		size = imagesize.Unset
	}
	return imagesize.Request{Size: size, Widths: imagesize.Widths{
		Poster:   q.PosterWidth,
		Backdrop: q.BackdropWidth,
		Logo:     q.LogoWidth,
		Still:    q.StillWidth,
		Photo:    q.PhotoWidth,
	}}
}

// widthsCursorFilter extends a pagination cursor's binding to image_size with
// the slot widths, so a continuation cannot mix sizes within one listing. It
// is empty without widths, which keeps cursors issued before they existed
// valid.
func (q ArtworkQuery) widthsCursorFilter() string {
	if widths := q.Artwork().Widths.Encode(); widths != "" {
		return "&image_widths=" + widths
	}
	return ""
}

// isArtworkQueryLocation reports whether a validation error points at one of
// the ArtworkQuery parameters, which stay query parameters even on operations
// whose other inputs travel in the body.
func isArtworkQueryLocation(location string) bool {
	name, ok := strings.CutPrefix(location, "query.")
	if !ok {
		return false
	}
	switch name {
	case "image_size", "poster_width", "backdrop_width", "logo_width", "still_width", "photo_width":
		return true
	default:
		return false
	}
}
