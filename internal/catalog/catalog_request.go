package catalog

import "time"

// CatalogSource identifies the catalog surface or exact source a request targets.
type CatalogSource string

const (
	CatalogSourceQuery             CatalogSource = "query"
	CatalogSourceSection           CatalogSource = "section"
	CatalogSourceLibraryCollection CatalogSource = "library_collection"
	CatalogSourceUserCollection    CatalogSource = "user_collection"
	CatalogSourceFavorites         CatalogSource = "favorites"
	CatalogSourceWatchlist         CatalogSource = "watchlist"
	CatalogSourceHistory           CatalogSource = "history"
	CatalogSourcePerson            CatalogSource = "person"
)

// CatalogRequest is the normalized request shape shared by catalog parsing and resolution.
type CatalogRequest struct {
	Source         CatalogSource
	Scope          string
	SectionID      string
	LibraryID      int
	CollectionID   string
	PersonID       int64
	NamePrefix     string
	SearchQuery    string
	Query          QueryDefinition
	Limit          int
	Offset         int
	UseSourceOrder bool
	SkipTotal      bool
	// SnapshotAt freezes the result set to items created at or before this
	// timestamp, preventing offset-based pagination drift when new items are
	// added during a scan.  Nil means the server will generate a snapshot.
	SnapshotAt *time.Time
	// ResolvedSort freezes saved/default sort resolution the same way SnapshotAt
	// freezes the result set. A caller that pages a single logical request
	// (grouped browse) sets it from the first page's EffectiveSort so a
	// preference edited mid-pagination cannot order later pages differently than
	// the order already advertised to the client. Nil resolves normally.
	ResolvedSort *QuerySort
}
