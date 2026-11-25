package jellycompat

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// visibleLibrariesContent is the direct content service with a fixed library
// list, so loadVisibleCollection sees the collection's library.
type visibleLibrariesContent struct {
	*directContentService
	libraries []upstreamUserLibrary
}

func (c *visibleLibrariesContent) ListUserLibraries(context.Context, *Session) ([]upstreamUserLibrary, error) {
	return c.libraries, nil
}

// seedBoxSetPageCollection stores an eight-member collection in a visible
// library. Curated position i holds the title "Title H" minus i, so SortName
// reverses the curated order. The member at position 2 lives only in a second
// library, the one at position 4 is rated 18, and the one at position 7 is in
// no library, as when the scanner's file removal grace holds a title whose
// file was moved away. It returns a handler whose
// viewer may see the first library up to PG-13, and the BoxSet's ParentId.
func seedBoxSetPageCollection(t *testing.T) (*ItemsHandler, string) {
	t.Helper()
	pool, suffix := jellyfin12CompatPool(t)
	ctx := context.Background()

	var visibleLib, hiddenLib int
	for _, target := range []*int{&visibleLib, &hiddenLib} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, TRUE) RETURNING id`,
			fmt.Sprintf("jf-boxset-page-%d-%p", suffix, target)).Scan(target); err != nil {
			t.Fatalf("seed folder: %v", err)
		}
	}

	type member struct {
		id, title string
		library   int
		ratingAge int
	}
	members := make([]member, 8)
	for i := range members {
		members[i] = member{id: fmt.Sprintf("jf-boxset-page-%d-m%d", suffix, i), title: fmt.Sprintf("Title %c", 'H'-i), library: visibleLib, ratingAge: 12}
	}
	members[2].library = hiddenLib // hidden by library access
	members[4].ratingAge = 18      // hidden by the rating ceiling
	members[7].library = 0         // in no library
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.id
	}

	collections := catalog.NewLibraryCollectionRepository(pool)
	collection, err := collections.Create(ctx, catalog.CreateLibraryCollectionInput{
		LibraryID: visibleLib, Slug: fmt.Sprintf("jf-boxset-page-%d", suffix), Title: "Page Test",
		CollectionType: "manual", Visibility: "visible",
	})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collections WHERE id = $1`, collection.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = ANY($1)`, []int{visibleLib, hiddenLib})
	})
	items := make([]catalog.LibraryCollectionItemInput, 0, len(members))
	for i, m := range members {
		jellyfin12Exec(t, pool, `INSERT INTO media_items (content_id, type, title, sort_title, content_rating, content_rating_age) VALUES ($1, 'movie', $2, $2, 'PG', $3)`, m.id, m.title, m.ratingAge)
		if m.library != 0 {
			jellyfin12Exec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, m.id, m.library)
		}
		items = append(items, catalog.LibraryCollectionItemInput{MediaItemID: m.id, SourceRank: i})
	}
	if err := collections.ReplaceItems(ctx, collection.ID, items); err != nil {
		t.Fatalf("fill collection: %v", err)
	}

	viewer := func(context.Context, int, string) catalog.AccessFilter {
		return catalog.AccessFilter{AllowedLibraryIDs: []int{visibleLib}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}
	}
	browse := catalog.NewBrowseRepository(pool)
	codec := NewResourceIDCodec()
	h := &ItemsHandler{
		content: &visibleLibrariesContent{
			directContentService: &directContentService{browseRepo: browse, accessFilter: viewer},
			libraries:            []upstreamUserLibrary{{ID: visibleLib, Name: "Movies", Type: "movies"}},
		},
		userData:     &mockUserDataService{},
		codec:        codec,
		mapper:       newMapper(codec, &config.Config{}),
		images:       NewImageCache(time.Hour, time.Now),
		collections:  collections,
		browseRepo:   browse,
		accessFilter: viewer,
	}
	return h, codec.EncodeStringID(EncodedIDCollection, collection.ID)
}

// TestBoxSetChildrenPageVisibleMembersDB: a stored BoxSet pages its members in
// SQL over only what the viewer can see. Members hidden by library access or
// by the rating ceiling sit in the middle of a page; they must neither shorten
// that page nor shift later pages, and TotalRecordCount counts visible members
// only, in curated order and with an explicit SortBy alike. Limit=0 returns
// only that total.
func TestBoxSetChildrenPageVisibleMembersDB(t *testing.T) {
	h, parentID := seedBoxSetPageCollection(t)

	page := func(query string) ([]string, int) {
		t.Helper()
		result := performItemsRequest(t, h, "/Items?ParentId="+url.QueryEscape(parentID)+query)
		names := make([]string, 0, len(result.Items))
		for _, item := range result.Items {
			if item.ParentID != parentID {
				t.Fatalf("%s: ParentId = %q, want %q", query, item.ParentID, parentID)
			}
			names = append(names, item.Name)
		}
		return names, result.TotalRecordCount
	}

	// Visible members in curated order: positions 0, 1, 3, 5, 6.
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"&StartIndex=0&Limit=3", []string{"Title H", "Title G", "Title E"}},
		{"&StartIndex=3&Limit=3", []string{"Title C", "Title B"}},
		{"&StartIndex=1&Limit=2", []string{"Title G", "Title E"}},
		{"&StartIndex=5&Limit=3", []string{}},
		{"&Limit=0", []string{}},
		{"&StartIndex=2&Limit=0", []string{}},
		{"", []string{"Title H", "Title G", "Title E", "Title C", "Title B"}},
		{"&SortBy=SortName&SortOrder=Ascending&StartIndex=0&Limit=3", []string{"Title B", "Title C", "Title E"}},
		{"&SortBy=SortName&SortOrder=Ascending&StartIndex=3&Limit=3", []string{"Title G", "Title H"}},
		{"&SortBy=SortName&SortOrder=Ascending&Limit=0", []string{}},
	} {
		names, total := page(tc.query)
		if !slices.Equal(names, tc.want) || total != 5 {
			t.Errorf("%q: got %v (total %d), want %v (total 5)", tc.query, names, total, tc.want)
		}
	}

	// A viewer without restrictions sees every member in a library.
	h.accessFilter = nil
	h.content.(*visibleLibrariesContent).accessFilter = nil
	if names, total := page("&StartIndex=0&Limit=10"); len(names) != 7 || total != 7 || slices.Contains(names, "Title A") {
		t.Errorf("unrestricted viewer: got %v (total %d), want the 7 members in a library", names, total)
	}
}

// TestBoxSetCountIgnoresHideWatchedDB: a server collection that hides watched
// titles from its native readers still counts, as a BoxSet, every member its
// children list, whichever profile reads it.
func TestBoxSetCountIgnoresHideWatchedDB(t *testing.T) {
	pool, suffix := jellyfin12CompatPool(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("jf-boxset-hide-watched-%d", suffix)
	var library, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, TRUE) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM library_collections WHERE slug = $1`, prefix)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, library)
	})
	profile := prefix + "-profile"
	jellyfin12Exec(t, pool, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'viewer')`, profile, userID)
	collections := catalog.NewLibraryCollectionRepository(pool)
	created, err := collections.Create(ctx, catalog.CreateLibraryCollectionInput{
		LibraryID: library, Slug: prefix, Title: prefix, CollectionType: "manual", Visibility: "visible", HideWatched: true,
	})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	for i := range 2 {
		id := fmt.Sprintf("%s-%d", prefix, i)
		jellyfin12Exec(t, pool, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', $1)`, id)
		jellyfin12Exec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, library)
		if err := collections.AddItem(ctx, created.ID, id, i); err != nil {
			t.Fatalf("add member: %v", err)
		}
	}
	jellyfin12Exec(t, pool, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at) VALUES ($1, $2, $3, 5400, 5400, TRUE, NOW())`,
		userID, profile, prefix+"-0")
	collection, err := collections.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}

	h := &ItemsHandler{
		collections: collections,
		accessFilter: func(_ context.Context, userID int, profileID string) catalog.AccessFilter {
			return catalog.AccessFilter{UserID: userID, ProfileID: profileID, AllowedLibraryIDs: []int{library}}
		},
	}
	shown := h.collectionsForViewer(ctx, &Session{StreamAppUserID: userID, ProfileID: profile}, []*models.LibraryCollection{collection})
	if len(shown) != 1 || shown[0].ItemCount != 2 {
		t.Fatalf("shown = %+v, want the collection counting both members", shown)
	}
}
