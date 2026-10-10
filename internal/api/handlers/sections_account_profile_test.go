package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// accountStores hands each account its own store, so a write addressed to
// the wrong account shows up as a write to the wrong store.
type accountStores map[int]userstore.UserStore

func (s accountStores) ForUser(_ context.Context, userID int) (userstore.UserStore, error) {
	store, ok := s[userID]
	if !ok {
		return nil, errors.New("no such account")
	}
	return store, nil
}

func (accountStores) Close() error { return nil }

func newAccountProfileStore(t *testing.T, name string, profiles ...string) userstore.UserStore {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.Name()+"-"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	store := userdb.NewSQLiteUserStore(db)
	for _, id := range profiles {
		if err := store.CreateProfile(context.Background(), userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatalf("create profile %s: %v", id, err)
		}
	}
	return store
}

// adminContext is an administrator (account 1, profile p-admin) acting on
// someone else's account.
func adminContext() context.Context {
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{Role: "admin", UserID: 1})
	return apimw.SetProfileID(ctx, "p-admin")
}

func seedOverrides(t *testing.T, store userstore.UserStore, profileID string, sectionIDs ...string) {
	t.Helper()
	rows := make([]userstore.SectionOverride, 0, len(sectionIDs))
	for i, id := range sectionIDs {
		pos := i
		rows = append(rows, userstore.SectionOverride{ID: profileID + "-" + id, ProfileID: profileID, Scope: "home", SectionID: id, Position: &pos})
	}
	if err := store.SaveSectionOverrides(context.Background(), profileID, "home", "", rows); err != nil {
		t.Fatalf("seed overrides: %v", err)
	}
}

func overrideSections(t *testing.T, store userstore.UserStore, profileID string) []string {
	t.Helper()
	rows, err := store.ListSectionOverrides(context.Background(), profileID, "home", "")
	if err != nil {
		t.Fatalf("list overrides: %v", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.SectionID)
	}
	return ids
}

func TestAccountProfileOverridesActOnlyOnTheAddressedProfile(t *testing.T) {
	family := newAccountProfileStore(t, "family", "p-main", "p-kid")
	other := newAccountProfileStore(t, "other", "p-other")
	h := &SectionHandler{StoreProvider: accountStores{7: family, 8: other}}
	seedOverrides(t, family, "p-main", "s-continue", "s-recent")
	seedOverrides(t, family, "p-kid", "s-recent")
	audited := captureAuditLogs(t)
	ctx := adminContext()
	q := SectionOverridesQuery{UserID: 7, ProfileID: "p-kid", Scope: "home"}

	rows, err := h.ListAccountProfileOverrides(ctx, q)
	if err != nil || len(rows) != 1 || rows[0].SectionID != "s-recent" {
		t.Fatalf("list = %+v, %v", rows, err)
	}

	pos, hidden := 0, true
	writes := []SectionOverrideWrite{
		{ID: "o-1", SectionID: "s-continue", Position: &pos, Hidden: hidden},
		{ID: "o-2", SectionID: "s-recent", Position: new(1)},
	}
	if err := h.SaveAccountProfileOverrides(ctx, q, writes); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := overrideSections(t, family, "p-kid"); len(got) != 2 {
		t.Fatalf("p-kid overrides = %v, want both writes", got)
	}
	if got := overrideSections(t, family, "p-main"); len(got) != 2 {
		t.Fatalf("p-main overrides changed: %v", got)
	}

	if err := h.ResetAccountProfileOverrides(ctx, q); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := overrideSections(t, family, "p-kid"); len(got) != 0 {
		t.Fatalf("p-kid overrides after reset = %v", got)
	}
	if got := overrideSections(t, family, "p-main"); len(got) != 2 {
		t.Fatalf("reset reached p-main: %v", got)
	}

	records := audited()
	if len(records) != 2 {
		t.Fatalf("emitted %d audit records, want 2: %+v", len(records), records)
	}
	for i, action := range []string{"set", "clear"} {
		r := records[i]
		if r["action"] != action || r["setting_key"] != profileSectionsAuditKey || r["scope"] != "home" ||
			r["target_profile_id"] != "p-kid" || r["target_user_id"] != float64(7) ||
			r["actor_user_id"] != float64(1) || r["actor_profile_id"] != "p-admin" || r["acting_as_admin"] != true {
			t.Errorf("record %d = %+v", i, r)
		}
	}
}

func TestAccountProfileOverridesRefuseAProfileOfAnotherAccount(t *testing.T) {
	family := newAccountProfileStore(t, "family", "p-main")
	other := newAccountProfileStore(t, "other", "p-other")
	h := &SectionHandler{StoreProvider: accountStores{7: family, 8: other}}
	seedOverrides(t, other, "p-other", "s-recent")
	audited := captureAuditLogs(t)
	ctx := adminContext()
	// p-other exists, but on account 8.
	q := SectionOverridesQuery{UserID: 7, ProfileID: "p-other", Scope: "home"}

	_, err := h.ListAccountProfileOverrides(ctx, q)
	requireAPIStatus(t, err, http.StatusNotFound)
	_, err = h.ResolveAccountProfileSectionSettings(ctx, q, nil)
	requireAPIStatus(t, err, http.StatusNotFound)
	requireAPIStatus(t, h.SaveAccountProfileOverrides(ctx, q, nil), http.StatusNotFound)
	requireAPIStatus(t, h.ResetAccountProfileOverrides(ctx, q), http.StatusNotFound)
	requireAPIStatus(t, h.ResetAccountProfileOverrides(ctx, SectionOverridesQuery{UserID: 7, Scope: "home"}), http.StatusNotFound)

	if got := overrideSections(t, other, "p-other"); len(got) != 1 {
		t.Fatalf("p-other overrides changed: %v", got)
	}
	if records := audited(); len(records) != 0 {
		t.Fatalf("audited a refused write: %+v", records)
	}
}

// An administrator changing their own profile's layout through the admin
// route is not acting for someone else, so it stays out of the trail.
func TestAccountProfileOverridesDoNotAuditTheAdminsOwnProfile(t *testing.T) {
	own := newAccountProfileStore(t, "own", "p-admin")
	h := &SectionHandler{StoreProvider: accountStores{1: own}}
	audited := captureAuditLogs(t)

	if err := h.ResetAccountProfileOverrides(adminContext(), SectionOverridesQuery{UserID: 1, ProfileID: "p-admin", Scope: "home"}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if records := audited(); len(records) != 0 {
		t.Fatalf("audited the admin's own profile: %+v", records)
	}
}

// An administrator's write runs the recipe gate with the owning account's
// role. Saving an admin-only recipe section onto a regular account's profile
// is refused, because that profile's own next save would be refused too.
func TestSaveAccountProfileOverridesGatesRecipesOnTheOwningAccount(t *testing.T) {
	store := newAccountProfileStore(t, "member", "p-member")
	h := &SectionHandler{StoreProvider: accountStores{2: store}}
	q := SectionOverridesQuery{UserID: 2, ProfileID: "p-member", Scope: "home"}
	writes := []SectionOverrideWrite{{IsUserAdded: true, UserSectionType: "admin_curated_list", UserConfig: []byte(`{"item_ids":["a"]}`)}}

	err := h.SaveAccountProfileOverrides(adminContext(), q, writes)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("SaveAccountProfileOverrides error = %v, want 403 custom_disabled", err)
	}
	if got := overrideSections(t, store, "p-member"); len(got) != 0 {
		t.Fatalf("refused save wrote overrides: %v", got)
	}
}

// A library page is addressed by the profile's account, so the
// administrator's own library access, which is their browsing, does not
// decide whether they can correct it.
func TestAccountProfileOverridesIgnoreTheAdminsLibraryAccess(t *testing.T) {
	store := newAccountProfileStore(t, "family", "p-kid")
	h := &SectionHandler{StoreProvider: accountStores{7: store}}
	ctx := access.SetScope(adminContext(), access.Scope{DisabledLibraryIDs: []int{5}})
	q := SectionOverridesQuery{UserID: 7, ProfileID: "p-kid", Scope: "library", LibraryID: "5"}

	if err := h.SaveAccountProfileOverrides(ctx, q, []SectionOverrideWrite{{ID: "o-1", SectionID: "s-recent", Position: new(0)}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	rows, err := h.ListAccountProfileOverrides(ctx, q)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list = %+v, %v", rows, err)
	}
	if err := h.ResetAccountProfileOverrides(ctx, q); err != nil {
		t.Fatalf("reset: %v", err)
	}

	// The profile's own routes still answer for the viewer's access.
	_, err = h.ListProfileOverrides(ctx, q)
	requireAPIStatus(t, err, http.StatusNotFound)
}
