package apiv2

import (
	"context"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
)

type fakeDownloadManifests struct {
	row                 *downloads.OfflineManifest
	page                downloads.ManifestPage
	user                int
	profile, device, id string
	limit               int
	after               *downloads.RegistryPosition
	err                 error
}

func (f *fakeDownloadManifests) BuildManifest(_ context.Context, user int, profile, device, id string, _ catalogpkg.AccessFilter) (*downloads.OfflineManifest, error) {
	f.user = user
	f.profile = profile
	f.device = device
	f.id = id
	return f.row, f.err
}
func (f *fakeDownloadManifests) PageBatchManifests(_ context.Context, user int, profile, device, id string, after *downloads.RegistryPosition, limit int, _ catalogpkg.AccessFilter) (downloads.ManifestPage, error) {
	f.user = user
	f.profile = profile
	f.device = device
	f.id = id
	f.limit = limit
	f.after = after
	return f.page, f.err
}
func syntheticDownloadManifest() *downloads.OfflineManifest {
	row := &downloads.OfflineManifest{DownloadID: "entry", ContentID: "movie", MediaFileID: 42, Revision: 2, Title: "Synthetic film", Quality: "original", EffectiveQuality: "original", DeliveryFormat: "original", FileSize: 1024, GeneratedAt: "2026-01-02T03:04:05Z", ManifestVersion: 2}
	row.ArtworkURLs.Poster = "/api/v2/downloads/entry/artwork/poster"
	row.Subtitles = []downloads.OfflineSubtitle{{Language: "en", Format: "vtt", FetchURL: "/api/v2/downloads/entry/subtitles/external:0"}}
	row.StableIdentity = downloads.OfflineIdentity{StableType: "movie", ProviderIDs: map[string]string{"tmdb": "42"}}
	row.Chapters = []downloads.OfflineChapter{{Index: 0, Title: "Chapter", StartSeconds: 0, EndSeconds: 30}}
	return row
}
