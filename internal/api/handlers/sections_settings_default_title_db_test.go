package handlers

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestProfileSectionSettingsKeepAdminTitleDB: the settings read over the
// real section repository and profile store returns the profile's rename as
// the title and the admin row's own title beside it, and follows a later
// admin rename for the default while the profile's rename stays.
func TestProfileSectionSettingsKeepAdminTitleDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(ctx, f.account)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "p1"}); err != nil {
		t.Fatal(err)
	}

	repo := sections.NewRepository(f.pool)
	library := f.library
	create := func(title string, position int) *sections.PageSection {
		t.Helper()
		s, err := repo.Create(ctx, &sections.PageSection{
			Scope: "library", LibraryID: &library, Position: position, SectionType: sections.SectionRecentlyAdded,
			Title: title, ItemLimit: 20, Config: json.RawMessage(`{}`), Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	renamed := create("Recently Added", 0)
	untouched := create("Favorites", 1)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM page_sections WHERE id = ANY($1)`, []string{renamed.ID, untouched.ID})
	})

	libraryKey := strconv.Itoa(library)
	position := 2
	if err := store.SaveSectionOverrides(ctx, "p1", "library", libraryKey, []userstore.SectionOverride{
		{ID: "o-renamed", SectionID: renamed.ID, Title: "Weekend picks"},
		{ID: "o-gems", IsUserAdded: true, UserSectionType: string(sections.SectionHiddenGems), UserTitle: "Hidden gems", Position: &position},
	}); err != nil {
		t.Fatal(err)
	}

	h := NewSectionHandler(repo, nil)
	h.StoreProvider = provider
	titles := func() map[string][2]string {
		t.Helper()
		resolved, err := h.ResolveProfileSectionSettings(ctx, f.account, "p1", "library", &library, catalog.AccessFilter{})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][2]string{}
		for _, s := range resolved {
			out[s.ID] = [2]string{s.Title, s.DefaultTitle}
		}
		return out
	}
	assert := func(want map[string][2]string) {
		t.Helper()
		got := titles()
		if len(got) != len(want) {
			t.Fatalf("settings = %q, want %q", got, want)
		}
		for id, w := range want {
			if got[id] != w {
				t.Errorf("section %s (title, default title) = %q, want %q", id, got[id], w)
			}
		}
	}

	assert(map[string][2]string{
		renamed.ID:   {"Weekend picks", "Recently Added"},
		untouched.ID: {"Favorites", "Favorites"},
		"o-gems":     {"Hidden gems", ""},
	})

	renamed.Title = "New this week"
	if err := repo.Update(ctx, renamed); err != nil {
		t.Fatal(err)
	}
	assert(map[string][2]string{
		renamed.ID:   {"Weekend picks", "New this week"},
		untouched.ID: {"Favorites", "Favorites"},
		"o-gems":     {"Hidden gems", ""},
	})
}
