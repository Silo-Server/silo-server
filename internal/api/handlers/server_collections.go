package handlers

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// serverCollectionsPerLibraryCap bounds how many collections each library
// contributes to the aggregated server-collections response. The frontend
// renders each library as a horizontal teaser row with a "See all" link into
// the per-library Collections tab, so it never needs the full set up front.
// Capping keeps the payload and the per-library presign cost bounded.
const serverCollectionsPerLibraryCap = 20

// serverCollectionsLibrary is one library's bucket of visible server (admin)
// collections. Collections is a capped teaser slice; TotalCount is the full
// visible count, used to drive the "See all (N)" affordance.
type serverCollectionsLibrary struct {
	LibraryID   int                    `json:"library_id"`
	LibraryName string                 `json:"library_name"`
	TotalCount  int                    `json:"total_count"`
	Collections []libraryTabCollection `json:"collections"`
}

type serverCollectionsResponse struct {
	Libraries []serverCollectionsLibrary `json:"libraries"`
}

// capServerCollections trims a library's collections to the per-library teaser
// cap and returns the capped slice alongside the original total count (used for
// the frontend's "See all (N)" affordance).
func capServerCollections(collections []*models.LibraryCollection) ([]*models.LibraryCollection, int) {
	total := len(collections)
	if total > serverCollectionsPerLibraryCap {
		return collections[:serverCollectionsPerLibraryCap], total
	}
	return collections, total
}

// HandleListServerCollections aggregates visible server (admin-curated library)
// collections across every library the requester can access, bucketed by
// library. It powers the "Server collections" section of the user-facing
// Collections tab, surfacing collections that otherwise live only inside each
// individual library's tab.
//
// This is intentionally a separate endpoint from GET /collections (which
// returns the user's editable personal collections): the two have different
// access semantics and cache/refetch lifecycles, and overloading the personal
// response with read-only server collections would be a client-facing trap.
func (h *LibraryCollectionHandler) HandleListServerCollections(w http.ResponseWriter, r *http.Request) {
	resp := serverCollectionsResponse{Libraries: []serverCollectionsLibrary{}}
	if h.FolderRepo == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	folders, err := h.FolderRepo.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load libraries")
		return
	}

	// Folders are already ordered by sort_order; iterate in that order so the
	// rendered rows match the user's library ordering.
	for _, f := range folders {
		if !f.Enabled {
			continue
		}
		// Honor the requester's library access scope; never reveal collections
		// from libraries outside AllowedLibraryIDs.
		if !requestCanAccessLibrary(r, f.ID) {
			continue
		}

		// ListByLibrary returns only visible collections, ordered
		// featured-first, so the capped slice surfaces the best ones.
		collections, err := h.repo.ListByLibrary(r.Context(), f.ID, catalog.ListLibraryCollectionsOptions{})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load collections")
			return
		}
		if len(collections) == 0 {
			continue
		}

		collections, total := capServerCollections(collections)

		colls := make([]libraryTabCollection, 0, len(collections))
		for _, c := range collections {
			colls = append(colls, libraryTabCollection{
				ID:              c.ID,
				Title:           c.Title,
				PosterURL:       h.presignGPURL(r, c.PosterURL),
				PosterThumbhash: c.PosterThumbhash,
				ItemCount:       c.ItemCount,
				Featured:        c.Featured,
			})
		}

		resp.Libraries = append(resp.Libraries, serverCollectionsLibrary{
			LibraryID:   f.ID,
			LibraryName: f.Name,
			TotalCount:  total,
			Collections: colls,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}
