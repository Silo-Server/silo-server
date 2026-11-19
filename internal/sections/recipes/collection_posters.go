package recipes

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxCollectionPosters is the most collections one row of collections
// lists.
const MaxCollectionPosters = 30

// CollectionRef names one collection a row of collections shows: exactly one
// of LibraryCollectionID (a server collection) or UserCollectionID (a
// personal collection).
type CollectionRef struct {
	LibraryCollectionID string `json:"library_collection_id,omitempty"`
	UserCollectionID    string `json:"user_collection_id,omitempty"`
}

// CollectionPostersParams is the config of a collection_posters row: the
// collections it shows, in order.
type CollectionPostersParams struct {
	Collections []CollectionRef `json:"collections"`
}

type collectionPostersRecipe struct{}

func (collectionPostersRecipe) Type() string                   { return "collection_posters" }
func (collectionPostersRecipe) NewParams() any                 { return &CollectionPostersParams{} }
func (collectionPostersRecipe) DefaultCacheTTL() time.Duration { return 0 }

// Resolve is never reached: the section fetcher builds this row's cards
// itself, and the row has no titles.
func (collectionPostersRecipe) Resolve(ResolverContext) (ResolvedItems, error) {
	return ResolvedItems{}, nil
}

func (collectionPostersRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("collection_posters: collections is required")
	}
	var p CollectionPostersParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("collection_posters: %w", err)
	}
	return ValidateCollectionRefs(p.Collections)
}

// ValidateCollectionRefs checks a row of collections' list: 1 to
// MaxCollectionPosters entries, each naming exactly one collection, none
// twice.
func ValidateCollectionRefs(refs []CollectionRef) error {
	if len(refs) == 0 {
		return errors.New("collection_posters: collections must name at least one collection")
	}
	if len(refs) > MaxCollectionPosters {
		return fmt.Errorf("collection_posters: collections holds at most %d collections", MaxCollectionPosters)
	}
	seen := make(map[CollectionRef]bool, len(refs))
	for _, ref := range refs {
		library, user := strings.TrimSpace(ref.LibraryCollectionID), strings.TrimSpace(ref.UserCollectionID)
		if (library == "") == (user == "") {
			return errors.New("collection_posters: each entry sets exactly one of library_collection_id or user_collection_id")
		}
		key := CollectionRef{LibraryCollectionID: library, UserCollectionID: user}
		if seen[key] {
			return errors.New("collection_posters: collections names a collection twice")
		}
		seen[key] = true
	}
	return nil
}

func (collectionPostersRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:     "collection_posters",
		Category: CategoryHandPicked,
		Presets: []GalleryPreset{
			{
				Key:              "collection_posters_pick",
				DisplayName:      "A row of collections",
				Icon:             "🗂️",
				DescriptionShort: "Show several collections as posters. Each one opens its collection.",
				DefaultParams:    json.RawMessage(`{"collections":[]}`),
			},
		},
	}
}

func init() {
	Register(collectionPostersRecipe{})
}
