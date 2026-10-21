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
