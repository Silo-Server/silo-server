package main

import (
	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// newCompatDetailService builds the catalog detail service the Jellyfin
// listener reads items through. Like the native API's, it resolves the
// viewer's stored subtitle, audio and version defaults through userStores;
// without them every Jellyfin item detail reads as having no preferences.
func newCompatDetailService(
	deps *api.Dependencies,
	itemRepo *catalog.ItemRepository,
	episodeRepo *catalog.EpisodeRepository,
	seasonRepo *catalog.SeasonRepository,
	personRepo *catalog.PersonRepository,
	files catalog.FileVersionFetcher,
	userStores userstore.UserStoreProvider,
) *catalog.DetailService {
	detailSvc := catalog.NewDetailService(itemRepo, episodeRepo, seasonRepo, personRepo, files)
	detailSvc.SetFolderRepository(deps.FolderRepo)
	detailSvc.SetGroupClaimRepository(catalog.NewGroupClaimRepository(deps.DB))
	detailSvc.SetProbeEnsurer(deps.ProbeEnsurer)
	detailSvc.SetChapterThumbnailQueuer(deps.ChapterThumbnailQueuer)
	if deps.ImageResolver != nil {
		detailSvc.SetImageResolver(deps.ImageResolver)
	}
	detailSvc.SetUserStoreProvider(userStores)
	return detailSvc
}
