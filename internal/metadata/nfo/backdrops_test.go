package nfo

import (
	"context"

	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

func discoverImages(t *testing.T, contentType, mediaPath, dir string) []metadata.RemoteImage {
	t.Helper()
	images, err := NewProvider().GetImages(context.Background(), metadata.ImageRequest{
		ContentType:               contentType,
		RepresentativeFilePath:    mediaPath,
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	return images
}
