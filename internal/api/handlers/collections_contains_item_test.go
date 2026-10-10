package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestPersonalCollectionsHoldingItemDB covers the Add to collection ticks:
// only the acting profile's own manual collections that hold the title, and
// nothing for a title the profile can't access, so the answer never reveals
// that such a title exists (#193 S1, S2, S3). The frozen /api/v1 list ignores
// the parameter.
func TestPersonalCollectionsHoldingItemDB(t *testing.T) {
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
	if err := f.pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("contains-item-%d", time.Now().UnixNano())).Scan(&otherLogin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, otherLogin) })
	// f.ids[0] lives in f.hidden, the rest in f.library; f.ids[2] is rated R.
	f.exec(t, `UPDATE media_items SET content_rating_age=17 WHERE content_id=$1`, f.ids[2])

	create := func(creator, name, kind string, shared bool, items ...string) string {
		t.Helper()
		c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
			CreatorProfileID: creator, Name: name, CollectionType: kind, QueryDefinition: "{}",
			IsShared: shared,
		})
		if err != nil {
			t.Fatal(err)
		}
		for i, item := range items {
			if err := store.AddCollectionItem(ctx, c.ID, item, i); err != nil {
				t.Fatal(err)
			}
		}
		return c.ID
	}
	holdsAll := create("owner", "Holds all", "manual", false, f.ids[0], f.ids[1], f.ids[2])
	sharedHolds := create("owner", "Shared", "manual", true, f.ids[1])
	create("owner", "Empty", "manual", false)
	create("owner", "Synced", "mdblist", false, f.ids[1])
	sharedWithOwner := create("viewer", "Viewer's", "manual", true, f.ids[1])

	// Production wraps the store provider in the notification decorator.
	h := NewCollectionHandler(notifications.WrapUserStoreProvider(provider, &notifications.System{}))
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}

	request := func(userID int, profileID string, scope *access.Scope) context.Context {
		reqCtx := apimw.SetClaims(ctx, &auth.Claims{UserID: userID})
		reqCtx = apimw.SetProfileID(reqCtx, profileID)
		if scope != nil {
			reqCtx = access.SetScope(reqCtx, *scope)
		}
		return reqCtx
	}
	holding := func(t *testing.T, reqCtx context.Context, userID int, profileID, itemID string) []string {
		t.Helper()
		got, err := h.PersonalCollectionsHoldingItem(reqCtx, userID, profileID, itemID)
		if err != nil {
			t.Fatal(err)
		}
		return slices.Sorted(maps.Keys(got))
	}
	sorted := func(ids ...string) []string { return slices.Sorted(slices.Values(ids)) }

	t.Run("only the profile's own manual collections that hold the title", func(t *testing.T) {
		got := holding(t, request(f.account, "owner", nil), f.account, "owner", f.ids[1])
		if want := sorted(holdsAll, sharedHolds); !slices.Equal(got, want) {
			t.Fatalf("holding = %v, want %v (not the synced list, not %s shared by viewer)", got, want, sharedWithOwner)
		}
		got = holding(t, request(f.account, "owner", nil), f.account, "owner", f.ids[3])
		if len(got) != 0 {
			t.Fatalf("title in no collection: holding = %v", got)
		}
	})

	t.Run("another profile's shared collection is never reported", func(t *testing.T) {
		got := holding(t, request(f.account, "viewer", nil), f.account, "viewer", f.ids[1])
		if want := []string{sharedWithOwner}; !slices.Equal(got, want) {
			t.Fatalf("viewer holding = %v, want only its own %v", got, want)
		}
	})

	t.Run("a title the profile cannot access reports nothing", func(t *testing.T) {
		libraries := &access.Scope{AllowedLibraryIDs: []int{f.library}, LibrariesRestricted: true}
		if got := holding(t, request(f.account, "owner", libraries), f.account, "owner", f.ids[0]); len(got) != 0 {
			t.Fatalf("title in a library the profile can't see: holding = %v", got)
		}
		if got := holding(t, request(f.account, "owner", libraries), f.account, "owner", f.ids[1]); !slices.Equal(got, sorted(holdsAll, sharedHolds)) {
			t.Fatalf("visible title under the same restriction: holding = %v", got)
		}
		rating := &access.Scope{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
		if got := holding(t, request(f.account, "owner", rating), f.account, "owner", f.ids[2]); len(got) != 0 {
			t.Fatalf("title above the rating ceiling: holding = %v", got)
		}
		if got := holding(t, request(f.account, "owner", nil), f.account, "owner", "no-such-title"); len(got) != 0 {
			t.Fatalf("unknown title: holding = %v", got)
		}
	})

	t.Run("another login reports nothing", func(t *testing.T) {
		if got := holding(t, request(otherLogin, "owner", nil), otherLogin, "owner", f.ids[1]); len(got) != 0 {
			t.Fatalf("other login holding = %v", got)
		}
	})

	t.Run("v1 list ignores contains_item", func(t *testing.T) {
		list := func(target string) []byte {
			t.Helper()
			req := httptest.NewRequest(http.MethodGet, target, nil)
			rec := httptest.NewRecorder()
			h.HandleListCollections(rec, req.WithContext(request(f.account, "owner", nil)))
			if rec.Code != http.StatusOK {
				t.Fatalf("v1 %s: %d %s", target, rec.Code, rec.Body.String())
			}
			return rec.Body.Bytes()
		}
		plain := list("/api/v1/collections")
		with := list("/api/v1/collections?contains_item=" + f.ids[1])
		if !bytes.Equal(plain, with) {
			t.Fatalf("v1 list changed with contains_item:\n%s\n%s", plain, with)
		}
		var body struct {
			Collections []map[string]json.RawMessage `json:"collections"`
		}
		if err := json.Unmarshal(with, &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Collections) == 0 {
			t.Fatalf("v1 list is empty: %s", with)
		}
		for _, c := range body.Collections {
			if _, ok := c["contains"]; ok {
				t.Fatalf("v1 collection carries contains: %v", c)
			}
		}
	})
}
