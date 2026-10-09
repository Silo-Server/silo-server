package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestPersonalCollectionCreateDescriptionDB covers the description a /api/v2
// create carries, the frozen /api/v1 create that ignores one, and #193 S2's
// isolation of the new collection from other profiles and other logins.
func TestPersonalCollectionCreateDescriptionDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(ctx, f.account)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"owner", "viewer"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	var otherLogin int
	if err := f.pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("create-description-%d", time.Now().UnixNano())).Scan(&otherLogin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, otherLogin) })
	h := NewCollectionHandler(provider)

	stored := func(t *testing.T, id string) string {
		t.Helper()
		c, err := store.GetCollection(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return c.Description
	}

	t.Run("create stores the description", func(t *testing.T) {
		created, err := h.CreatePersonalCollection(ctx, PersonalCollectionCreateCommand{UserID: f.account, ProfileID: "owner", Request: PersonalCollectionCreateRequest{
			Name: "Rainy days", Description: "For wet afternoons",
		}})
		if err != nil {
			t.Fatal(err)
		}
		if created.Description != "For wet afternoons" {
			t.Fatalf("created description = %q", created.Description)
		}
		if got := stored(t, created.ID); got != "For wet afternoons" {
			t.Fatalf("stored description = %q", got)
		}
		read, err := h.GetPersonalCollection(ctx, f.account, "owner", created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if read.Description != "For wet afternoons" {
			t.Fatalf("read description = %q", read.Description)
		}
	})

	t.Run("create without a description stores none", func(t *testing.T) {
		created, err := h.CreatePersonalCollection(ctx, PersonalCollectionCreateCommand{UserID: f.account, ProfileID: "owner", Request: PersonalCollectionCreateRequest{Name: "No description"}})
		if err != nil {
			t.Fatal(err)
		}
		if created.Description != "" || stored(t, created.ID) != "" {
			t.Fatalf("description = %q, stored %q", created.Description, stored(t, created.ID))
		}
	})

	// /api/v1 is frozen: its create never took a description, and a client
	// that sends one still gets a collection without it.
	v1Create := func(t *testing.T, req *http.Request) PersonalCollectionView {
		t.Helper()
		reqCtx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: f.account})
		reqCtx = apimw.SetProfileID(reqCtx, "owner")
		rec := httptest.NewRecorder()
		h.HandleCreateCollection(rec, req.WithContext(reqCtx))
		if rec.Code != http.StatusCreated {
			t.Fatalf("v1 create: %d %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"description"`) {
			t.Fatalf("v1 create echoed a description: %s", rec.Body.String())
		}
		var created PersonalCollectionView
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		return created
	}
	t.Run("v1 JSON create ignores a description", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/collections", strings.NewReader(`{"name":"v1 JSON","description":"ignored"}`))
		req.Header.Set("Content-Type", "application/json")
		created := v1Create(t, req)
		if created.Name != "v1 JSON" || stored(t, created.ID) != "" {
			t.Fatalf("name %q, stored description %q", created.Name, stored(t, created.ID))
		}
	})
	t.Run("v1 multipart create ignores a description", func(t *testing.T) {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		if err := form.WriteField("data", `{"name":"v1 multipart","description":"ignored"}`); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/collections", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		created := v1Create(t, req)
		if created.Name != "v1 multipart" || stored(t, created.ID) != "" {
			t.Fatalf("name %q, stored description %q", created.Name, stored(t, created.ID))
		}
	})

	// #193 S2: a private collection made with a description is invisible to
	// another profile on the account and to another login; a shared one can be
	// read but not changed by a profile that doesn't own it.
	requireStatus := func(t *testing.T, err error, status int) {
		t.Helper()
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != status {
			t.Fatalf("error = %v, want status %d", err, status)
		}
	}
	rename := "Taken over"
	t.Run("another profile cannot read or change a private collection", func(t *testing.T) {
		private, err := h.CreatePersonalCollection(ctx, PersonalCollectionCreateCommand{UserID: f.account, ProfileID: "owner", Request: PersonalCollectionCreateRequest{Name: "Private", Description: "Mine"}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = h.GetPersonalCollection(ctx, f.account, "viewer", private.ID)
		requireStatus(t, err, http.StatusNotFound)
		_, err = h.UpdatePersonalCollection(ctx, PersonalCollectionUpdateCommand{UserID: f.account, ProfileID: "viewer", CollectionID: private.ID, Request: PersonalCollectionUpdateRequest{Description: &rename}})
		requireStatus(t, err, http.StatusNotFound)
		list, err := h.ListPersonalCollections(ctx, f.account, "viewer")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range list.Collections {
			if c.ID == private.ID {
				t.Fatalf("viewer lists the owner's private collection %s", c.ID)
			}
		}
		_, err = h.GetPersonalCollection(ctx, otherLogin, "owner", private.ID)
		requireStatus(t, err, http.StatusNotFound)
		if got := stored(t, private.ID); got != "Mine" {
			t.Fatalf("description after refused writes = %q", got)
		}
	})
	t.Run("another profile cannot change a shared collection", func(t *testing.T) {
		shared, err := h.CreatePersonalCollection(ctx, PersonalCollectionCreateCommand{UserID: f.account, ProfileID: "owner", Request: PersonalCollectionCreateRequest{
			Name: "Shared", Description: "Ours", IsShared: true,
		}})
		if err != nil {
			t.Fatal(err)
		}
		read, err := h.GetPersonalCollection(ctx, f.account, "viewer", shared.ID)
		if err != nil {
			t.Fatal(err)
		}
		if read.Description != "Ours" {
			t.Fatalf("viewer reads description %q", read.Description)
		}
		_, err = h.UpdatePersonalCollection(ctx, PersonalCollectionUpdateCommand{UserID: f.account, ProfileID: "viewer", CollectionID: shared.ID, Request: PersonalCollectionUpdateRequest{Description: &rename}})
		requireStatus(t, err, http.StatusForbidden)
		_, err = h.GetPersonalCollection(ctx, otherLogin, "viewer", shared.ID)
		requireStatus(t, err, http.StatusNotFound)
		if got := stored(t, shared.ID); got != "Ours" {
			t.Fatalf("description after refused write = %q", got)
		}
	})
}

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
