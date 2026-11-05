package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// serverCollectionsPerLibraryCap bounds how many collections each library
// contributes to the aggregated server-collections response. The web
// Collections page opens with these and loads the rest of a library's list
// from its Collections tab only when the viewer picks that library or scrolls
// to the end, so this answer never needs the full set. Capping keeps the
// payload and the per-library presign cost bounded.
const serverCollectionsPerLibraryCap = 20

// ServerCollectionsLibraryView is one library's bucket of visible server (admin)
// collections. Collections is a capped teaser slice; TotalCount is the full
// visible count.
type ServerCollectionsLibraryView struct {
	LibraryID   int                    `json:"library_id"`
	LibraryName string                 `json:"library_name"`
	TotalCount  int                    `json:"total_count"`
	Collections []libraryTabCollection `json:"collections"`
}

type ServerCollectionsView struct {
	Libraries []ServerCollectionsLibraryView `json:"libraries"`
}

// capServerCollections trims a library's collections to the per-library teaser
// cap and returns the capped slice alongside the original total count (used for
// the web Collections page to tell whether a library has more to load).
func capServerCollections(collections []*models.LibraryCollection) ([]*models.LibraryCollection, int) {
	total := len(collections)
	if total > serverCollectionsPerLibraryCap {
		return collections[:serverCollectionsPerLibraryCap], total
	}
	return collections, total
}

func (h *LibraryCollectionHandler) ListServerCollections(ctx context.Context, access catalog.AccessFilter) (ServerCollectionsView, error) {
	resp := ServerCollectionsView{Libraries: []ServerCollectionsLibraryView{}}
	if h.FolderRepo == nil {
		return resp, nil
	}

	folders, err := h.FolderRepo.List(ctx)
	if err != nil {
		return ServerCollectionsView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load libraries")
	}

	// Folders are already ordered by sort_order; iterate in that order so the
	// rendered rows match the user's library ordering.
	type libraryCollections struct {
		folder      *models.MediaFolder
		collections []*models.LibraryCollection
	}
	var listed []libraryCollections
	var all []*models.LibraryCollection
	seen := map[string]bool{}
	for _, f := range folders {
		if !f.Enabled {
			continue
		}
		// Honor the requester's library access scope; never reveal collections
		// from libraries outside AllowedLibraryIDs.
		if !viewerCanAccessLibrary(ctx, f.ID) {
			continue
		}

		// ListByLibrary returns only visible collections, ordered
		// featured-first, so the capped slice surfaces the best ones.
		collections, err := h.repo.ListByLibrary(ctx, f.ID, catalog.ListLibraryCollectionsOptions{})
		if err != nil {
			return ServerCollectionsView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load collections")
		}
		listed = append(listed, libraryCollections{folder: f, collections: collections})
		for _, c := range collections {
			if !seen[c.ID] {
				seen[c.ID] = true
				all = append(all, c)
			}
		}
	}

	// A collection in several libraries is counted once. Viewing comes before
	// the cap so the total counts only what the viewer is shown.
	counts := h.viewerCollectionCounts(ctx, all, access)
	for _, l := range listed {
		collections := catalog.CollectionsShownToViewer(l.collections, counts)
		if len(collections) == 0 {
			continue
		}

		collections, total := capServerCollections(collections)
		collections = h.withViewerPosters(ctx, collections, access)

		colls := make([]libraryTabCollection, 0, len(collections))
		for _, c := range collections {
			colls = append(colls, h.libraryTabCardOf(ctx, c))
		}

		resp.Libraries = append(resp.Libraries, ServerCollectionsLibraryView{
			LibraryID:   l.folder.ID,
			LibraryName: l.folder.Name,
			TotalCount:  total,
			Collections: colls,
		})
	}

	return resp, nil
}
