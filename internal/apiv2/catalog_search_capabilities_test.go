package apiv2

import (
	"context"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

func (f *fakeCatalog) SearchContinuationCapabilities(context.Context) (catalogpkg.SearchContinuationCapabilities, error) {
	return catalogpkg.SearchContinuationCapabilities{Provider: "meilisearch", ResultWindowLimit: 1000, SessionTTLSeconds: 900, MaxSessionsPerAccount: 16}, f.err
}
