package catalog

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/models"
)

type VersionMarkerPreview struct {
	models.MarkerSegment
	ThumbnailURL            string
	ThumbnailThumbhash      string
	ThumbnailCaptureSeconds float64
}

// BuildMarkerPreviews projects only current occurrences and resolves protected
// URLs through the same artwork resolver used by embedded chapters.
func BuildMarkerPreviews(ctx context.Context, file *models.MediaFile, resolve func(context.Context, []string) map[string]ResolvedImageURL) []VersionMarkerPreview {
	markers := models.EffectiveMarkerThumbnails(file)
	paths := make([]string, 0, len(markers))
	for _, marker := range markers {
		if marker.ThumbnailPath != "" {
			paths = append(paths, marker.ThumbnailPath)
		}
	}
	var urls map[string]ResolvedImageURL
	if resolve != nil && len(paths) > 0 {
		urls = resolve(ctx, paths)
	}
	out := make([]VersionMarkerPreview, 0, len(markers))
	for _, marker := range markers {
		if url := urls[marker.ThumbnailPath].URL; url != "" {
			out = append(out, VersionMarkerPreview{MarkerSegment: models.MarkerSegment{Kind: marker.Kind, StartSeconds: marker.StartSeconds, EndSeconds: marker.EndSeconds}, ThumbnailURL: url, ThumbnailThumbhash: marker.ThumbnailThumbhash, ThumbnailCaptureSeconds: marker.StartSeconds})
		}
	}
	return out
}
