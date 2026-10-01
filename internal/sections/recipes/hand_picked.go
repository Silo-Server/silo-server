package recipes

import (
	"encoding/json"
	"errors"
	"time"
)

// CollectionParams is the typed config for a collection section. Exactly one
// of LibraryCollectionID (admin-curated) or UserCollectionID (profile-scoped
// personal collection) must be set — the dispatcher picks the right backend
// path based on which field is populated.
type CollectionParams struct {
	LibraryCollectionID string `json:"library_collection_id,omitempty"`
	UserCollectionID    string `json:"user_collection_id,omitempty"`
}

type collectionRecipe struct{}

func (collectionRecipe) Type() string                   { return "collection" }
func (collectionRecipe) NewParams() any                 { return &CollectionParams{} }
func (collectionRecipe) DefaultCacheTTL() time.Duration { return 10 * time.Minute }
func (collectionRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("collection", rc)
}
func (collectionRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("collection: missing collection id")
	}
	var p CollectionParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.LibraryCollectionID == "" && p.UserCollectionID == "" {
		return errors.New("collection: library_collection_id or user_collection_id is required")
	}
	return nil
}
func (collectionRecipe) Definition() RecipeDefinition {
	presets := []GalleryPreset{
		{
			Key:              "collection_pick",
			DisplayName:      "Collection",
			Icon:             "📁",
			DescriptionShort: "Show items from a library or curated collection.",
			DefaultParams:    json.RawMessage(`{"library_collection_id":""}`),
		},
	}

	return RecipeDefinition{
		Type:     "collection",
		Category: CategoryHandPicked,
		Presets:  presets,
	}
}

func init() {
	Register(collectionRecipe{})
}
