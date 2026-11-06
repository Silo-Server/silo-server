package handlers

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestPersonalCollectionCreateDescriptionUnsupportedStore covers a store with
// no description column: a create carrying one is refused rather than stored
// without it, and a create without one still succeeds.
func TestPersonalCollectionCreateDescriptionUnsupportedStore(t *testing.T) {
	const account = 1
	db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "user.db"), account)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := userdb.NewSQLiteUserStore(db.DB)
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "owner", Name: "owner"}); err != nil {
		t.Fatal(err)
	}
	h := NewCollectionHandler(pagingIntegrationProvider{account: account, store: store})

	_, err = h.CreatePersonalCollection(t.Context(), PersonalCollectionCreateCommand{UserID: account, ProfileID: "owner", Request: PersonalCollectionCreateRequest{
		Name: "Rainy days", Description: "For wet afternoons",
	}})
	if apiErr := (*APIError)(nil); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotImplemented {
		t.Fatalf("error = %v, want status %d", err, http.StatusNotImplemented)
	}
	list, err := store.ListCollections(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("refused create stored %d collections", len(list))
	}
	if _, err := h.CreatePersonalCollection(t.Context(), PersonalCollectionCreateCommand{UserID: account, ProfileID: "owner", Request: PersonalCollectionCreateRequest{Name: "Plain"}}); err != nil {
		t.Fatal(err)
	}
	features, err := h.PersonalCollectionFeatures(t.Context(), account)
	if err != nil {
		t.Fatal(err)
	}
	if features.Description {
		t.Fatal("SQLite store reports description support")
	}
}

// Renaming a personal collection retitles the rows that still carry its old
// name on every profile of the login. A row with a typed title, and a row for
// another collection that happens to share the name, keep theirs.
func TestPersonalCollectionRenameRetitlesItsRows(t *testing.T) {
	const account = 1
	db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "user.db"), account)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := userdb.NewSQLiteUserStore(db.DB)
	for _, id := range []string{"owner", "kid"} {
		if err := store.CreateProfile(t.Context(), userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewCollectionHandler(pagingIntegrationProvider{account: account, store: store})
	created, err := h.CreatePersonalCollection(t.Context(), PersonalCollectionCreateCommand{UserID: account, ProfileID: "owner", Request: PersonalCollectionCreateRequest{Name: "Rainy days"}})
	if err != nil {
		t.Fatal(err)
	}
	row := func(id, collectionID, title string) userstore.SectionOverride {
		return userstore.SectionOverride{ID: id, IsUserAdded: true, UserSectionType: "collection", UserTitle: title,
			UserConfig: `{"user_collection_id":"` + collectionID + `"}`}
	}
	if err := store.SaveSectionOverrides(t.Context(), "owner", "home", "", []userstore.SectionOverride{
		row("follows", created.ID, "Rainy days"),
		row("typed", created.ID, "Wet weekend"),
		row("other", "another-collection", "Rainy days"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSectionOverrides(t.Context(), "kid", "home", "", []userstore.SectionOverride{
		row("kid-follows", created.ID, "Rainy days"),
	}); err != nil {
		t.Fatal(err)
	}

	renamed := "Stormy nights"
	if _, err := h.UpdatePersonalCollection(t.Context(), PersonalCollectionUpdateCommand{UserID: account, ProfileID: "owner", CollectionID: created.ID, Request: PersonalCollectionUpdateRequest{Name: &renamed}}); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"follows": "Stormy nights", "typed": "Wet weekend", "other": "Rainy days", "kid-follows": "Stormy nights"}
	got := map[string]string{}
	for _, profile := range []string{"owner", "kid"} {
		overrides, err := store.ListSectionOverrides(t.Context(), profile, "home", "")
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range overrides {
			got[o.ID] = o.UserTitle
		}
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for id, title := range want {
		if got[id] != title {
			t.Errorf("row %s title = %q, want %q", id, got[id], title)
		}
	}
}
