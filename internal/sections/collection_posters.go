package sections

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

// A row of collections (SectionCollectionPosters) shows collections as cards
// instead of titles. Its config lists the collections in order
// (recipes.CollectionPostersParams). The row has no items: the fetcher fills
// SectionWithItems.Collections with the cards the viewer may see, built by
// the Fetcher's CollectionCards source, and TotalCount is their number. The
// row's item limit does not apply, and it is never cached: it is at most
// recipes.MaxCollectionPosters cards read with a few batched queries.

// Collection card sources.
const (
	CollectionCardLibrary = "library"
	CollectionCardUser    = "user"
)

// CollectionCard is one card of a row of collections, as the viewer sees it.
type CollectionCard struct {
	ID string
	// Source is CollectionCardLibrary for a server collection or
	// CollectionCardUser for a personal one.
	Source string
	Title  string
	// PosterURL is the signed card-size poster: the assigned poster, else
	// the viewer's collage, else empty.
	PosterURL       string
	PosterThumbhash string
	PosterIsCollage bool
	// BackdropURL is the signed wide backdrop of a server collection that
	// has one, else empty. Personal collections have no backdrop.
	BackdropURL       string
	BackdropThumbhash string
	// ItemCount is the number of titles the viewer sees in it.
	ItemCount int
	// LibraryID is, for a server collection, a library the viewer can open
	// that holds it, so a client can build the collection's page link.
	LibraryID *int
	// CreatorProfileID is the profile that owns a personal collection.
	CreatorProfileID *string
}

// CollectionCardScope is who reads a row of collections and on which page.
type CollectionCardScope struct {
	// LibraryID is the library page the row is on, or nil on Home. On a
	// library page only the server collections in that library are shown.
	LibraryID *int
	UserID    int
	ProfileID string
	Filter    catalog.AccessFilter
	// AdminRow is true for an admin page row, which shows server
	// collections only.
	AdminRow bool
}

// CollectionCardSource builds the cards of a row of collections: one per
// ref, in ref order, leaving out every collection the viewer may not see or
// that no longer exists.
type CollectionCardSource interface {
	CollectionCards(ctx context.Context, refs []recipes.CollectionRef, scope CollectionCardScope) ([]CollectionCard, error)
}

// HoldsCollections reports whether rows of type t show collections rather
// than titles.
func HoldsCollections(t SectionType) bool {
	return t == SectionCollectionPosters
}

// CanBeHero reports whether a row of type t may be a page's hero banner.
// A hero shows one title, so a row of collections can't be one.
func CanBeHero(t SectionType) bool {
	return !HoldsCollections(t)
}

// HasCards reports whether a fetched row has anything to show: titles, or
// collections for a row of collections.
func (s SectionWithItems) HasCards() bool {
	return len(s.Items) > 0 || len(s.Collections) > 0
}

// ParseCollectionPostersConfig returns the collections a row of collections
// config lists, trimmed, in order. A config that does not parse lists none.
func ParseCollectionPostersConfig(config json.RawMessage) []recipes.CollectionRef {
	var p recipes.CollectionPostersParams
	if len(config) == 0 || json.Unmarshal(config, &p) != nil {
		return nil
	}
	out := make([]recipes.CollectionRef, 0, len(p.Collections))
	for _, ref := range p.Collections {
		ref.LibraryCollectionID = strings.TrimSpace(ref.LibraryCollectionID)
		ref.UserCollectionID = strings.TrimSpace(ref.UserCollectionID)
		if ref.LibraryCollectionID == "" && ref.UserCollectionID == "" {
			continue
		}
		out = append(out, ref)
	}
	return out
}

// LibraryCollectionIDsOf returns every server collection a section config
// references: a collection row's library_collection_id and each entry of a
// row of collections. These are the references that block deleting a
// collection and that section writes lock.
func LibraryCollectionIDsOf(config json.RawMessage) []string {
	ids := []string{}
	if len(config) == 0 {
		return ids
	}
	var cfg struct {
		ID          string `json:"library_collection_id"`
		Collections []struct {
			ID string `json:"library_collection_id"`
		} `json:"collections"`
	}
	if json.Unmarshal(config, &cfg) != nil {
		// A config whose collections list is malformed still references its
		// scalar collection.
		var scalar struct {
			ID string `json:"library_collection_id"`
		}
		if json.Unmarshal(config, &scalar) == nil && scalar.ID != "" {
			ids = append(ids, scalar.ID)
		}
		return ids
	}
	if cfg.ID != "" {
		ids = append(ids, cfg.ID)
	}
	for _, entry := range cfg.Collections {
		if entry.ID != "" && !slices.Contains(ids, entry.ID) {
			ids = append(ids, entry.ID)
		}
	}
	return ids
}

// ErrUserCollectionInAdminRow is returned when an admin page row of
// collections names a personal collection.
var ErrUserCollectionInAdminRow = errors.New("collection_posters: a server row shows server collections only; remove user_collection_id entries")

// ValidateAdminCollectionPosters checks that an admin page row of collections
// names server collections only. Other section types pass.
func ValidateAdminCollectionPosters(t SectionType, config json.RawMessage) error {
	if t != SectionCollectionPosters {
		return nil
	}
	for _, ref := range ParseCollectionPostersConfig(config) {
		if ref.UserCollectionID != "" {
			return ErrUserCollectionInAdminRow
		}
	}
	return nil
}

// fetchCollectionPosters builds a row of collections' cards for the viewer.
func (f *Fetcher) fetchCollectionPosters(ctx context.Context, s ResolvedSection, libraryID *int, userID int, profileID string, filter catalog.AccessFilter) (SectionWithItems, error) {
	result := SectionWithItems{ResolvedSection: s, Items: []*models.MediaItem{}, Collections: []CollectionCard{}}
	refs := ParseCollectionPostersConfig(s.Config)
	if len(refs) == 0 || f.CollectionCards == nil {
		return result, nil
	}
	release, err := acquireCollectionCardGate(ctx)
	if err != nil {
		return SectionWithItems{}, err
	}
	defer release()
	cards, err := f.CollectionCards.CollectionCards(ctx, refs, CollectionCardScope{
		LibraryID: libraryID,
		UserID:    userID,
		ProfileID: profileID,
		Filter:    filter,
		AdminRow:  !s.IsCustom,
	})
	if err != nil {
		return SectionWithItems{}, err
	}
	if cards != nil {
		result.Collections = cards
	}
	result.TotalCount = len(result.Collections)
	return result, nil
}

type collectionCardGateKey struct{}

// WithCollectionCardGate makes the rows of collections fetched under ctx
// build their cards one row at a time. A read that fetches rows concurrently
// (FetchAll, the Home layout's included rows) installs it. A row's cards already count and
// collage several collections at once, so the rows of one aggregate read,
// fetched concurrently, would otherwise multiply that into more database
// connections than one request should hold.
func WithCollectionCardGate(ctx context.Context) context.Context {
	return context.WithValue(ctx, collectionCardGateKey{}, make(chan struct{}, 1))
}

// acquireCollectionCardGate waits for the gate ctx carries, if any, and
// answers its release.
func acquireCollectionCardGate(ctx context.Context) (func(), error) {
	gate, ok := ctx.Value(collectionCardGateKey{}).(chan struct{})
	if !ok {
		return func() {}, nil
	}
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
