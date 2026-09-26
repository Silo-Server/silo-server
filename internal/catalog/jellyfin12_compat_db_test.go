package catalog

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func jellyfin12TestPool(t *testing.T) (*pgxpool.Pool, int, int64) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	suffix := time.Now().UnixNano()
	var libraryID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, TRUE) RETURNING id`,
		fmt.Sprintf("jf12-%d", suffix)).Scan(&libraryID); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, libraryID)
	})
	return pool, libraryID, suffix
}

// TestLibraryCollectionListContainingItemDB: the reverse membership lookup
// behind GET /Items/{id}/Collections returns only visible collections that
// store the item, in case-insensitive title order.
func TestLibraryCollectionListContainingItemDB(t *testing.T) {
	pool, libraryID, suffix := jellyfin12TestPool(t)
	ctx := context.Background()
	member := fmt.Sprintf("jf12-member-%d", suffix)
	other := fmt.Sprintf("jf12-other-%d", suffix)
	seedSortableItem(t, pool, member, "Member", 2001)
	seedSortableItem(t, pool, other, "Other", 2002)

	repo := NewLibraryCollectionRepository(pool)
	create := func(slug, title, visibility string, items ...string) string {
		t.Helper()
		c, err := repo.Create(ctx, CreateLibraryCollectionInput{LibraryID: libraryID, Slug: fmt.Sprintf("%s-%d", slug, suffix), Title: title, CollectionType: "manual", Visibility: visibility})
		if err != nil {
			t.Fatalf("create %s: %v", slug, err)
		}
		t.Cleanup(func() { _ = repo.Delete(context.Background(), c.ID) })
		inputs := make([]LibraryCollectionItemInput, 0, len(items))
		for _, id := range items {
			inputs = append(inputs, LibraryCollectionItemInput{MediaItemID: id})
		}
		if err := repo.ReplaceItems(ctx, c.ID, inputs); err != nil {
			t.Fatalf("replace items %s: %v", slug, err)
		}
		return c.ID
	}
	zeta := create("zeta", "zeta Set", "visible", member, other)
	alpha := create("alpha", "Alpha Set", "visible", member)
	create("hidden", "Hidden Set", "hidden", member)
	create("unrelated", "Unrelated", "visible", other)

	got, err := repo.ListContainingItem(ctx, member)
	if err != nil {
		t.Fatalf("ListContainingItem: %v", err)
	}
	ids := make([]string, 0, len(got))
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	if !slices.Equal(ids, []string{alpha, zeta}) {
		t.Fatalf("collections containing member = %v, want [%s %s]", ids, alpha, zeta)
	}
	if got[1].ItemCount != 2 {
		t.Fatalf("zeta ItemCount = %d, want 2", got[1].ItemCount)
	}
}

// TestBrowseLanguageFiltersDB: Jellyfin 12 audioLanguages/subtitleLanguages
// match the stored canonical language arrays and external subtitles, accept
// ISO 639-2 input, and ignore files that are missing.
func TestBrowseLanguageFiltersDB(t *testing.T) {
	pool, libraryID, suffix := jellyfin12TestPool(t)
	ctx := context.Background()
	english := fmt.Sprintf("jf12-en-%d", suffix)
	japanese := fmt.Sprintf("jf12-ja-%d", suffix)
	gone := fmt.Sprintf("jf12-gone-%d", suffix)
	seed := func(id, audio, subtitles, external string, missing bool) {
		t.Helper()
		seedSortableItem(t, pool, id, id, 2010)
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, libraryID); err != nil {
			t.Fatalf("seed library membership: %v", err)
		}
		var missingSince *time.Time
		if missing {
			now := time.Now()
			missingSince = &now
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_files (content_id, media_folder_id, file_path, audio_tracks, subtitle_tracks, external_subtitles, missing_since)
			VALUES ($1, $2, $1 || '.mkv', $3::jsonb, $4::jsonb, $5::jsonb, $6)`, id, libraryID, audio, subtitles, external, missingSince); err != nil {
			t.Fatalf("seed media file: %v", err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE content_id = $1`, id) })
	}
	seed(english, `[{"language":"eng"}]`, `[{"language":"fre"}]`, `[]`, false)
	seed(japanese, `[{"language":"jpn"}]`, `[]`, `[{"language":"spa"}]`, false)
	seed(gone, `[{"language":"eng"}]`, `[]`, `[]`, true)

	repo := NewBrowseRepository(pool)
	browse := func(filters BrowseFilters) []string {
		t.Helper()
		filters.Type = "movie"
		filters.LibraryID = libraryID
		filters.Limit = 10
		result, err := repo.Browse(ctx, filters)
		if err != nil {
			t.Fatalf("browse %+v: %v", filters, err)
		}
		ids := make([]string, 0, len(result.Items))
		for _, item := range result.Items {
			ids = append(ids, item.ContentID)
		}
		slices.Sort(ids)
		return ids
	}
	cases := []struct {
		name    string
		filters BrowseFilters
		want    []string
	}{
		{"iso 639-2 audio", BrowseFilters{AudioLanguages: []string{"eng"}}, []string{english}},
		{"canonical audio", BrowseFilters{AudioLanguages: []string{"ja"}}, []string{japanese}},
		{"any of several", BrowseFilters{AudioLanguages: []string{"jpn", "en"}}, []string{english, japanese}},
		{"embedded subtitle", BrowseFilters{SubtitleLanguages: []string{"fre"}}, []string{english}},
		{"external subtitle", BrowseFilters{SubtitleLanguages: []string{"spa"}}, []string{japanese}},
		{"canonical code matches an ISO 639-2 external subtitle", BrowseFilters{SubtitleLanguages: []string{"es"}}, []string{japanese}},
		{"canonical code matches a bibliographic embedded subtitle", BrowseFilters{SubtitleLanguages: []string{"fr"}}, []string{english}},
		{"both filters intersect", BrowseFilters{AudioLanguages: []string{"en"}, SubtitleLanguages: []string{"es"}}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := browse(tc.filters); !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPersonSearchVisibleWithOptionsDB: Jellyfin 12 /Persons name bounds
// compare lowercased names, and library/item scoping keeps only people
// credited there.
func TestPersonSearchVisibleWithOptionsDB(t *testing.T) {
	pool, libraryID, suffix := jellyfin12TestPool(t)
	ctx := context.Background()
	inLibrary := fmt.Sprintf("jf12-p-in-%d", suffix)
	elsewhere := fmt.Sprintf("jf12-p-out-%d", suffix)
	seedSortableItem(t, pool, inLibrary, "In", 2001)
	seedSortableItem(t, pool, elsewhere, "Out", 2002)
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, inLibrary, libraryID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	base := suffix % 1_000_000_000
	names := []string{"Alice Zeta", "bob Young", "Carol Xu", "Dave Out"}
	ids := make([]int64, len(names))
	for i, name := range names {
		ids[i] = base*10 + int64(i)
		if _, err := pool.Exec(ctx, `INSERT INTO people (id, name) VALUES ($1, $2)`, ids[i], fmt.Sprintf("%s %d", name, suffix)); err != nil {
			t.Fatalf("seed person: %v", err)
		}
		content := inLibrary
		if i == 3 {
			content = elsewhere
		}
		if _, err := pool.Exec(ctx, `INSERT INTO item_people (id, content_id, person_id, kind) VALUES ($1, $2, $1, 1)`, ids[i], content); err != nil {
			t.Fatalf("seed credit: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM item_people WHERE person_id = ANY($1)`, ids)
		_, _ = pool.Exec(context.Background(), `DELETE FROM people WHERE id = ANY($1)`, ids)
	})
	repo := NewPersonRepository(pool)
	search := func(opts PersonSearchOptions) ([]int64, int) {
		t.Helper()
		opts.Term = fmt.Sprintf("%d", suffix)
		opts.Limit = 10
		opts.IncludeTotal = true
		people, total, err := repo.SearchVisibleWithOptions(ctx, opts)
		if err != nil {
			t.Fatalf("search %+v: %v", opts, err)
		}
		got := make([]int64, 0, len(people))
		for _, p := range people {
			got = append(got, p.ID)
		}
		return got, total
	}
	if got, total := search(PersonSearchOptions{LibraryID: libraryID}); !slices.Equal(got, ids[:3]) || total != 3 {
		t.Fatalf("library scope = %v (total %d), want %v", got, total, ids[:3])
	}
	if got, _ := search(PersonSearchOptions{ContentID: elsewhere}); !slices.Equal(got, ids[3:]) {
		t.Fatalf("item scope = %v, want %v", got, ids[3:])
	}
	if got, _ := search(PersonSearchOptions{NameStartsWith: "B"}); !slices.Equal(got, ids[1:2]) {
		t.Fatalf("NameStartsWith B = %v", got)
	}
	if got, _ := search(PersonSearchOptions{NameLessThan: "C"}); !slices.Equal(got, ids[:2]) {
		t.Fatalf("NameLessThan C = %v", got)
	}
	if got, _ := search(PersonSearchOptions{NameStartsWithOrGreater: "c"}); !slices.Equal(got, ids[2:]) {
		t.Fatalf("NameStartsWithOrGreater c = %v", got)
	}
}

// TestBrowseLanguageFiltersRespectFileAccessDB: a language carried only by a
// file the viewer may not play (hidden library or above the quality ceiling)
// neither matches the filter nor appears in the scoped facets.
func TestBrowseLanguageFiltersRespectFileAccessDB(t *testing.T) {
	pool, visibleLibrary, suffix := jellyfin12TestPool(t)
	ctx := context.Background()
	var hiddenLibrary int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, TRUE) RETURNING id`, fmt.Sprintf("jf12-hidden-%d", suffix)).Scan(&hiddenLibrary); err != nil {
		t.Fatalf("seed hidden library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, hiddenLibrary)
	})
	movie := fmt.Sprintf("jf12-access-%d", suffix)
	seedSortableItem(t, pool, movie, movie, 2011)
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2), ($1, $3)`, movie, visibleLibrary, hiddenLibrary); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	files := []struct {
		folder     int
		resolution string
		audio      string
	}{
		{visibleLibrary, "1080p", `[{"language":"eng"}]`},
		{visibleLibrary, "2160p", `[{"language":"ger"}]`},
		{hiddenLibrary, "1080p", `[{"language":"fre"}]`},
	}
	for i, f := range files {
		if _, err := pool.Exec(ctx, `INSERT INTO media_files (content_id, media_folder_id, file_path, resolution, audio_tracks) VALUES ($1, $2, $1 || $3, $4, $5::jsonb)`,
			movie, f.folder, fmt.Sprintf("-%d.mkv", i), f.resolution, f.audio); err != nil {
			t.Fatalf("seed file: %v", err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE content_id = $1`, movie) })

	repo := NewBrowseRepository(pool)
	viewer := BrowseFilters{Type: "movie", ContentIDs: []string{movie}, LibraryIDs: []int{visibleLibrary}, MaxPlaybackQuality: "1080p", Limit: 10}
	matches := func(language string) bool {
		t.Helper()
		filters := viewer
		filters.AudioLanguages = []string{language}
		result, err := repo.Browse(ctx, filters)
		if err != nil {
			t.Fatalf("browse %s: %v", language, err)
		}
		return len(result.Items) == 1
	}
	if !matches("en") {
		t.Fatal("the playable English file must match")
	}
	if matches("fr") {
		t.Fatal("French exists only in a hidden library and must not match")
	}
	if matches("de") {
		t.Fatal("German exists only above the viewer's quality ceiling and must not match")
	}

	facet := viewer
	facet.ScopeFacetFilesToAccess = true
	languages, err := repo.ListAudioLanguages(ctx, facet)
	if err != nil {
		t.Fatalf("audio facet: %v", err)
	}
	if !slices.Equal(languages, []string{"en"}) {
		t.Fatalf("scoped audio facet = %v, want [en]", languages)
	}
	facet.ScopeFacetFilesToAccess = false
	if languages, err = repo.ListAudioLanguages(ctx, facet); err != nil || len(languages) != 3 {
		t.Fatalf("unscoped facet keeps every present file: %v (%v)", languages, err)
	}
}
