package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

type sortPrefLibraryReader struct {
	collection *models.LibraryCollection
}

func (r sortPrefLibraryReader) GetByID(_ context.Context, id string) (*models.LibraryCollection, error) {
	if r.collection == nil || r.collection.ID != id {
		return nil, catalog.ErrLibraryCollectionNotFound
	}
	return r.collection, nil
}

// These exercise the sort-preference endpoints through real HTTP handlers and a
// real store, using middleware.SetClaims / SetProfileID to supply the auth
// context the middleware would normally attach. Set SILO_TEST_DATABASE_URL to a
// migrated database to run them.

func sortPrefHandlerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	var tableName *string
	if err := pool.QueryRow(context.Background(),
		`SELECT to_regclass('public.user_collection_sort_preferences')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check preference table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied the collection sort preference migration")
	}
	return pool
}

func sortPrefFixtureUser(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var userID int
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO users (username, email, password_hash, role)
		VALUES ('sort-pref-handler-fixture', 'sort-pref-handler@invalid.test', '', 'user')
		RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatalf("seed fixture user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	return userID
}

// authed attaches the user + profile scope the middleware chain would normally
// have put on the request.
func authed(r *http.Request, userID int, profileID string) *http.Request {
	ctx := middleware.SetClaims(r.Context(), &auth.Claims{UserID: userID, Role: "user"})
	ctx = middleware.SetProfileID(ctx, profileID)
	return r.WithContext(ctx)
}

func TestCollectionSortPreferenceEndpoints(t *testing.T) {
	pool := sortPrefHandlerPool(t)
	userID := sortPrefFixtureUser(t, pool)
	const profileID = "profile-http"
	const collectionID = "http-collection-1"

	provider := pgstore.NewPostgresProvider(pool)
	handler := NewCollectionHandler(provider)
	handler.LibraryCollections = sortPrefLibraryReader{collection: &models.LibraryCollection{
		ID: collectionID, Visibility: "visible", LibraryIDs: []int{7},
	}}
	store, err := provider.ForUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: profileID, Name: "HTTP sort preference"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	personalCollection, err := store.CreateCollection(context.Background(), userstore.CreateCollectionInput{
		CreatorProfileID: profileID,
		Name:             "HTTP personal sort preference",
		CollectionType:   "manual",
	})
	if err != nil {
		t.Fatalf("seed personal collection: %v", err)
	}
	t.Cleanup(func() {
		_ = store.DeleteProfile(context.Background(), profileID)
	})

	put := func(body string) *httptest.ResponseRecorder {
		req := authed(httptest.NewRequest(http.MethodPut, "/collections/sort-preference", strings.NewReader(body)), userID, profileID)
		rec := httptest.NewRecorder()
		handler.HandleSetCollectionSortPreference(rec, req)
		return rec
	}

	t.Run("saves a sort the viewer picked", func(t *testing.T) {
		rec := put(`{"collection_kind":"library","collection_id":"` + collectionID + `","field":"title","order":"asc"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var resp collectionSortPreferenceResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Field != "title" || resp.Order != "asc" {
			t.Fatalf("response = %+v", resp)
		}

		// Read it back through the store, not the response, so the assertion
		// covers persistence rather than echo.
		pref, err := store.GetCollectionSortPreference(context.Background(), profileID, userstore.CollectionKindLibrary, collectionID)
		if err != nil || pref == nil {
			t.Fatalf("stored preference = %v, err = %v", pref, err)
		}
		if pref.SortField != "title" || pref.SortOrder != "asc" {
			t.Fatalf("stored preference = %+v", pref)
		}
	})

	t.Run("fills in the field's default order", func(t *testing.T) {
		rec := put(`{"collection_kind":"library","collection_id":"` + collectionID + `","field":"year"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		pref, _ := store.GetCollectionSortPreference(context.Background(), profileID, userstore.CollectionKindLibrary, collectionID)
		if pref == nil || pref.SortOrder != "desc" {
			t.Fatalf("stored preference = %+v, want year/desc", pref)
		}
	})

	t.Run("an empty field pins to source order", func(t *testing.T) {
		rec := put(`{"collection_kind":"library","collection_id":"` + collectionID + `","field":""}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		pref, _ := store.GetCollectionSortPreference(context.Background(), profileID, userstore.CollectionKindLibrary, collectionID)
		if pref == nil {
			t.Fatal("pinning to source order removed the row; it must persist as an explicit choice")
		}
		if pref.SortField != "" {
			t.Fatalf("stored field = %q, want empty", pref.SortField)
		}
	})

	t.Run("accepts a personalized viewer sort on a library collection", func(t *testing.T) {
		rec := put(`{"collection_kind":"library","collection_id":"` + collectionID + `","field":"progress"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		pref, _ := store.GetCollectionSortPreference(context.Background(), profileID, userstore.CollectionKindLibrary, collectionID)
		if pref == nil || pref.SortField != "progress" || pref.SortOrder != "desc" {
			t.Fatalf("stored preference = %+v, want progress/desc", pref)
		}
	})

	t.Run("rejects an invalid sort order", func(t *testing.T) {
		rec := put(`{"collection_kind":"library","collection_id":"` + collectionID + `","field":"title","order":"sideways"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("accepts a personalized sort on a personal collection", func(t *testing.T) {
		rec := put(`{"collection_kind":"user","collection_id":"` + personalCollection.ID + `","field":"progress"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		t.Cleanup(func() {
			_ = store.ClearCollectionSortPreference(context.Background(), profileID, userstore.CollectionKindUser, personalCollection.ID)
		})
	})

	t.Run("rejects a nonexistent collection", func(t *testing.T) {
		rec := put(`{"collection_kind":"library","collection_id":"missing","field":"title"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("rejects a library collection outside the viewer scope", func(t *testing.T) {
		req := authed(httptest.NewRequest(http.MethodPut, "/collections/sort-preference",
			strings.NewReader(`{"collection_kind":"library","collection_id":"`+collectionID+`","field":"title"}`)), userID, profileID)
		req = req.WithContext(access.SetScope(req.Context(), access.Scope{AllowedLibraryIDs: []int{99}}))
		rec := httptest.NewRecorder()
		handler.HandleSetCollectionSortPreference(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("rejects an unknown collection kind", func(t *testing.T) {
		rec := put(`{"collection_kind":"nonsense","collection_id":"x","field":"title"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	for _, kind := range []string{userstore.CollectionKindWatchlist, userstore.CollectionKindFavorites} {
		t.Run("saves personal source sort for "+kind, func(t *testing.T) {
			rec := put(`{"collection_kind":"` + kind + `","collection_id":"ignored","field":"added_at"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			pref, err := store.GetCollectionSortPreference(context.Background(), profileID, kind, userstore.PersonalSortPreferenceCollectionID)
			if err != nil || pref == nil || pref.SortField != "added_at" || pref.SortOrder != "desc" {
				t.Fatalf("stored preference = %+v, err = %v", pref, err)
			}

			// collection_id is optional and any supplied value is ignored.
			rec = put(`{"collection_kind":"` + kind + `","field":"title","order":"asc"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("omitted collection_id status = %d, body = %s", rec.Code, rec.Body.String())
			}

			for _, field := range []string{"last_air_date", "runtime"} {
				rec = put(`{"collection_kind":"` + kind + `","field":"` + field + `"}`)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s status = %d, body = %s", field, rec.Code, rec.Body.String())
				}
			}

			for _, field := range []string{"progress", "relevance", "not_a_sort"} {
				rec = put(`{"collection_kind":"` + kind + `","field":"` + field + `"}`)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("unsupported %s status = %d, want 400; body = %s", field, rec.Code, rec.Body.String())
				}
			}

			req := authed(httptest.NewRequest(http.MethodDelete,
				"/collections/sort-preference?collection_kind="+kind, nil), userID, profileID)
			rec = httptest.NewRecorder()
			handler.HandleClearCollectionSortPreference(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("clear status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}

	t.Run("clearing removes the override", func(t *testing.T) {
		req := authed(httptest.NewRequest(http.MethodDelete,
			"/collections/sort-preference?collection_kind=library&collection_id="+collectionID, nil), userID, profileID)
		rec := httptest.NewRecorder()
		handler.HandleClearCollectionSortPreference(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		pref, err := store.GetCollectionSortPreference(context.Background(), profileID, userstore.CollectionKindLibrary, collectionID)
		if err != nil {
			t.Fatalf("reading cleared preference: %v", err)
		}
		if pref != nil {
			t.Fatalf("preference still present after clear: %+v", pref)
		}
	})
}

func TestNormalizePersonalSortPreferenceRequest(t *testing.T) {
	for _, kind := range []string{userstore.CollectionKindWatchlist, userstore.CollectionKindFavorites} {
		if gotKind, gotID, ok := normalizeCollectionRef(kind, "ignored"); !ok || gotKind != kind || gotID != userstore.PersonalSortPreferenceCollectionID {
			t.Fatalf("normalizeCollectionRef(%q) = %q/%q/%v", kind, gotKind, gotID, ok)
		}
		for _, valid := range []struct {
			field, wantOrder string
		}{
			{field: "added_at", wantOrder: "desc"},
			{field: "last_air_date", wantOrder: "desc"},
			{field: "runtime", wantOrder: "desc"},
		} {
			if field, order, ok := normalizeSortPreference(kind, valid.field, ""); !ok || field != valid.field || order != valid.wantOrder {
				t.Fatalf("normalizeSortPreference(%q, %s) = %q/%q/%v", kind, valid.field, field, order, ok)
			}
		}
		for _, invalid := range []struct{ field, order string }{
			{field: "progress"},
			{field: "relevance"},
			{field: "not_a_sort"},
			{field: "title", order: "sideways"},
		} {
			if _, _, ok := normalizeSortPreference(kind, invalid.field, invalid.order); ok {
				t.Fatalf("normalizeSortPreference(%q, %q, %q) accepted invalid sort", kind, invalid.field, invalid.order)
			}
		}
	}
}

// TestNormalizeCollectionSortConfigForCreators covers the validation the
// collection create/update handlers apply to a creator-configured default.
func TestNormalizeCollectionSortConfigForCreators(t *testing.T) {
	cases := []struct {
		name              string
		raw               string
		allowPersonalized bool
		want              string
		wantErr           bool
	}{
		{name: "absent", raw: "", want: "{}"},
		{name: "explicit null", raw: `null`, want: "{}"},
		{name: "legacy empty object", raw: `{}`, want: "{}"},
		{name: "empty field remains source order", raw: `{"field":""}`, want: `{"field":""}`},
		{name: "preserves manual pins mode", raw: `{"mode":"manual_pins"}`, want: `{"mode":"manual_pins"}`},
		{name: "canonicalizes", raw: `{"field":"  Title ","order":"ASC"}`, want: `{"field":"title","order":"asc"}`},
		{name: "fills the default order", raw: `{"field":"year"}`, want: `{"field":"year","order":"desc"}`},
		{name: "rejects unknown fields", raw: `{"field":"not_a_sort"}`, wantErr: true},
		{name: "rejects invalid orders", raw: `{"field":"year","order":"sideways"}`, wantErr: true},
		{name: "rejects personalized for library", raw: `{"field":"progress"}`, wantErr: true},
		{
			name:              "allows personalized for personal",
			raw:               `{"field":"progress"}`,
			allowPersonalized: true,
			want:              `{"field":"progress","order":"desc"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeCollectionSortConfig(json.RawMessage(tc.raw), tc.allowPersonalized)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestNormalizeCollectionSortConfigPreservesManualPinsAcrossUpdates(t *testing.T) {
	const manualPins = `{"mode":"manual_pins"}`
	created, err := NormalizeCollectionSortConfig(json.RawMessage(manualPins), false)
	if err != nil {
		t.Fatalf("normalize on create: %v", err)
	}
	updated, err := NormalizeCollectionSortConfig(json.RawMessage(created), false)
	if err != nil {
		t.Fatalf("normalize on update: %v", err)
	}
	if updated != manualPins {
		t.Fatalf("manual pins config changed across create/read/update: got %s, want %s", updated, manualPins)
	}
}
