package userstore

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
)

// collectionRowConfigKey is the section config key that names the collection
// a profile-built collection row shows, per collection kind.
var collectionRowConfigKey = map[string]string{
	CollectionKindLibrary: "library_collection_id",
	CollectionKindUser:    "user_collection_id",
}

// RenameCollectionRowTitle gives a profile-built collection row the
// collection's new name when the row still carries the old one. A row is
// titled with its collection's name when it is added, so a title that still
// equals the old name follows the collection; any other title was typed and
// stays. It reads the fields the section resolver shows (User* first, then the
// legacy ones) and reports whether it changed the row.
func RenameCollectionRowTitle(o *SectionOverride, kind, collectionID, oldTitle, newTitle string) bool {
	key, ok := collectionRowConfigKey[kind]
	if !ok || o.SectionID != "" || collectionID == "" {
		return false
	}
	oldTitle, newTitle = strings.TrimSpace(oldTitle), strings.TrimSpace(newTitle)
	if oldTitle == "" || newTitle == "" || oldTitle == newTitle {
		return false
	}
	sectionType, config, title := o.SectionType, o.Config, &o.Title
	if o.UserSectionType != "" {
		sectionType = o.UserSectionType
	}
	if o.UserConfig != "" {
		config = o.UserConfig
	}
	if o.UserTitle != "" {
		title = &o.UserTitle
	}
	if sectionType != "collection" || strings.TrimSpace(*title) != oldTitle {
		return false
	}
	var refs map[string]any
	if json.Unmarshal([]byte(config), &refs) != nil {
		return false
	}
	if id, _ := refs[key].(string); strings.TrimSpace(id) != collectionID {
		return false
	}
	*title = newTitle
	return true
}

// CollectionRowRenamer carries a library collection's rename into the rows
// profiles built for it on their own pages, in every account. Library
// collections live in the central catalog while section overrides may live in
// shared PostgreSQL or per-user SQLite, so one query cannot reach them all.
type CollectionRowRenamer struct {
	users  UserLister
	stores UserStoreProvider
	logger *slog.Logger
}

// NewCollectionRowRenamer wires the cross-account rename sweep.
func NewCollectionRowRenamer(users UserLister, stores UserStoreProvider) *CollectionRowRenamer {
	return &CollectionRowRenamer{
		users:  users,
		stores: stores,
		logger: slog.Default().With("component", "userstore.collection_row_titles"),
	}
}

// RenameForCollection renames every account's rows for one library
// collection. Failures are logged per account and do not undo the rename; a
// row left behind keeps its old title, as before this sweep existed.
func (r *CollectionRowRenamer) RenameForCollection(ctx context.Context, collectionID, oldTitle, newTitle string) {
	if r == nil || r.users == nil || r.stores == nil {
		return
	}
	forEachUserStore(ctx, r.users, r.stores, r.logger, "collection row rename", []any{"collection_id", collectionID}, func(_ int, store UserStore) error {
		return store.RenameCollectionRows(ctx, CollectionKindLibrary, collectionID, oldTitle, newTitle)
	})
}
