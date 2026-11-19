package catalog

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// audiobookGroupsFixture is one audiobook library, a second library some of
// its books are also in, and a user with a viewer and a sibling profile.
type audiobookGroupsFixture struct {
	pool                   *pgxpool.Pool
	library, other, userID int
	prefix, viewer         string
}

func (f audiobookGroupsFixture) list(t *testing.T, q AudiobookGroupsQuery, filter AccessFilter) AudiobookGroupsResult {
	t.Helper()
	q.LibraryID = f.library
	result, err := ListAudiobookGroups(t.Context(), f.pool, q, filter)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f audiobookGroupsFixture) access() AccessFilter {
	return AccessFilter{UserID: f.userID, ProfileID: f.viewer}
}

// seedAudiobookGroups seeds Stephen King (seven books in the library, one
// credited under a padded spelling of his name, one more only in the other
// library), Ann Leckie and Ben Aaronovitch (two books each, one of Leckie's
// also in the other library), and Zed Author (one book). Five King books form
// The Dark Tower series. The viewer has one King book in progress and one
// finished; the sibling profile has progress on three more King books and on
// a Leckie book.
func seedAudiobookGroups(t *testing.T) audiobookGroupsFixture {
	t.Helper()
	pool := newBatchEquivTestPool(t)
	ctx := t.Context()
	prefix := fmt.Sprintf("abgroups-%d", time.Now().UnixNano())
	f := audiobookGroupsFixture{pool: pool, prefix: prefix, viewer: prefix + "-viewer"}
	for _, row := range []struct {
		name string
		id   *int
	}{{prefix, &f.library}, {prefix + "-other", &f.other}} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('audiobooks', $1, true) RETURNING id`, row.name).Scan(row.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	id := func(name string) string { return prefix + "-" + name }
	personBase := time.Now().UnixNano()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"-%")
		_, _ = pool.Exec(ctx, `DELETE FROM people WHERE id BETWEEN $1 AND $1 + 10`, personBase)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{f.library, f.other})
	})

	people := map[string]int64{}
	for i, name := range []string{"Stephen King", "  Stephen King ", "Ann Leckie", "Ben Aaronovitch", "Zed Author"} {
		people[name] = personBase + int64(i)
		batchEquivExec(t, pool, `INSERT INTO people (id, name) VALUES ($1, $2)`, people[name], name)
	}
	// Each King book's duration is a distinct power of ten, so a missing or
	// doubled book shows in the sum. An empty poster path means no poster.
	books := []struct {
		name, sortTitle, poster, author string
		duration                        int64
		libraries                       []int
	}{
		{"carrie", "Carrie", "carrie.jpg", "Stephen King", 1, []int{f.library}},
		{"misery", "Misery", "misery.jpg", "Stephen King", 10, []int{f.library}},
		{"it", "It", "", "Stephen King", 100, []int{f.library}},
		{"cujo", "Cujo", "", "Stephen King", 1000, []int{f.library}},
		{"tie-a", "Christine", "christine-a.jpg", "  Stephen King ", 0, []int{f.library}},
		{"tie-b", "Christine", "christine-b.jpg", "Stephen King", 10000, []int{f.library}},
		{"thinner", "Thinner", "thinner.jpg", "Stephen King", 100000, []int{f.library}},
		{"outside", "Outside", "outside.jpg", "Stephen King", 7, []int{f.other}},
		{"justice", "Ancillary Justice", "justice.jpg", "Ann Leckie", 50, []int{f.library, f.other}},
		{"sword", "Ancillary Sword", "sword.jpg", "Ann Leckie", 50, []int{f.library}},
		{"rivers", "Rivers of London", "rivers.jpg", "Ben Aaronovitch", 70, []int{f.library}},
		{"soho", "Moon Over Soho", "soho.jpg", "Ben Aaronovitch", 70, []int{f.library}},
		{"zed", "Zed", "zed.jpg", "Zed Author", 5, []int{f.library}},
	}
	for i, b := range books {
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, sort_title, poster_path) VALUES ($1, 'audiobook', $2, $2, $3)`, id(b.name), b.sortTitle, b.poster)
		for _, lib := range b.libraries {
			batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id(b.name), lib)
			if b.duration > 0 {
				batchEquivExec(t, pool, `INSERT INTO audiobook_item_file_stats (media_folder_id, content_id, duration_seconds, active_file_count) VALUES ($1, $2, $3, 1)
					ON CONFLICT (media_folder_id, content_id) DO UPDATE SET duration_seconds = EXCLUDED.duration_seconds`, lib, id(b.name), b.duration)
			}
		}
		batchEquivExec(t, pool, `INSERT INTO item_people (id, content_id, person_id, kind) VALUES ($1, $2, $3, 7)`, personBase+int64(i), id(b.name), people[b.author])
	}
	for _, s := range []struct {
		book  string
		index any
	}{{"carrie", 2}, {"misery", 1}, {"it", 3}, {"thinner", nil}, {"tie-b", nil}} {
		batchEquivExec(t, pool, `INSERT INTO audiobook_series (content_id, series_name, series_index) VALUES ($1, 'The Dark Tower', $2)`, id(s.book), s.index)
	}

	sibling := prefix + "-sibling"
	batchEquivExec(t, pool, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $3, 'viewer'), ($2, $3, 'sibling')`, f.viewer, sibling, f.userID)
	for _, p := range []struct {
		profile, book string
		completed     bool
	}{
		{f.viewer, "carrie", false},
		{f.viewer, "misery", true},
		{sibling, "it", true},
		{sibling, "cujo", true},
		{sibling, "thinner", true},
		{sibling, "justice", false},
	} {
		batchEquivExec(t, pool, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed) VALUES ($1, $2, $3, 10, 100, $4)`, f.userID, p.profile, id(p.book), p.completed)
	}
	return f
}

func audiobookGroupKeys(groups []AudiobookGroup) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.GroupKey)
	}
	return out
}

func audiobookCovers(names ...string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+".jpg")
	}
	return out
}

// TestAudiobookGroupsDB checks the grouped audiobook browse against real
// rows: item counts and durations per group, the four-book cover stack and its
// order (books without a poster skipped, a sort-title tie broken by content
// ID), progress from the requesting profile only, count-sorted cursor pages
// across an item-count tie, the search prefix, and library access when the
// viewer may not see the browsed library itself. It runs every check on the
// live query, before audiobook_group_stats holds the library, and again on
// the view once refreshed, so the two sources cannot drift apart. Set
// SILO_TEST_DATABASE_URL to a migrated disposable database; every inserted
// row is removed on completion.
func TestAudiobookGroupsDB(t *testing.T) {
	f := seedAudiobookGroups(t)
	t.Run("live", func(t *testing.T) { checkAudiobookGroups(t, f, false) })
	if _, err := RefreshAudiobookGroupStats(t.Context(), f.pool, 0); err != nil {
		t.Fatal(err)
	}
	t.Run("precomputed", func(t *testing.T) { checkAudiobookGroups(t, f, true) })

	// A book that leaves the library stays in the view until the next
	// refresh, so a viewer with library limits must not read the view: here
	// Ancillary Justice remains only in a library the viewer may not see.
	t.Run("book left the library since the refresh", func(t *testing.T) {
		batchEquivExec(t, f.pool, `DELETE FROM media_item_libraries WHERE content_id = $1 AND media_folder_id = $2`, f.prefix+"-justice", f.library)
		disabled, allowed := f.access(), f.access()
		disabled.DisabledLibraryIDs = []int{f.other}
		allowed.AllowedLibraryIDs = []int{f.library}
		for _, filter := range []AccessFilter{disabled, allowed} {
			result := f.list(t, AudiobookGroupsQuery{GroupBy: AudiobookGroupByAuthor, SearchPrefix: "ann", CursorPaging: true}, filter)
			if len(result.Groups) != 1 || result.Groups[0].ItemCount != 1 || slices.Contains(result.Groups[0].PosterPaths, "justice.jpg") {
				t.Fatalf("Leckie groups %+v for access %+v, want one with only Ancillary Sword", result.Groups, filter)
			}
		}
	})
}

// checkAudiobookGroups runs the browse checks. precomputed says the view
// holds the library, so it answers each viewer whose access hides none of
// the library's books.
func checkAudiobookGroups(t *testing.T, f audiobookGroupsFixture, precomputed bool) {
	list := func(t *testing.T, q AudiobookGroupsQuery, filter AccessFilter, viewSees bool) AudiobookGroupsResult {
		t.Helper()
		q.CursorPaging = true
		q.LibraryID = f.library
		_, served, err := listAudiobookGroupsFromStats(t.Context(), f.pool, q, filter, audiobookGroupsPageCap)
		if err != nil {
			t.Fatal(err)
		}
		if want := precomputed && viewSees; served != want {
			t.Fatalf("view answered %v, want %v", served, want)
		}
		result := f.list(t, q, filter)
		if served {
			// A viewer with a long progress history has the page's books
			// looked up one by one; the answer must not change.
			plan, err := buildAudiobookGroupStatsSQL(q, filter, audiobookGroupsPageCap, true)
			if err != nil {
				t.Fatal(err)
			}
			probed, err := runAudiobookGroupsPlan(t.Context(), f.pool, q, plan)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(probed, result) {
				t.Fatalf("progress looked up per book gave %+v, want %+v", probed, result)
			}
		}
		return result
	}

	t.Run("count-sorted cursor pages", func(t *testing.T) {
		q := AudiobookGroupsQuery{GroupBy: AudiobookGroupByAuthor, Sort: "count", Limit: 2, IncludeTotal: true}
		first := list(t, q, f.access(), true)
		if got, want := audiobookGroupKeys(first.Groups), []string{"stephen king", "ann leckie"}; !slices.Equal(got, want) || !first.HasMore || first.Total != 4 {
			t.Fatalf("first page %v (more %v, total %d), want %v with more of 4", got, first.HasMore, first.Total, want)
		}
		if first.Next == nil || *first.Next != (AudiobookGroupCursor{GroupKey: "ann leckie", Value: 2}) {
			t.Fatalf("next cursor %+v, want ann leckie at 2", first.Next)
		}
		checkKingGroup(t, first.Groups[0])
		if leckie := first.Groups[1]; leckie.InProgressCount != 0 || leckie.FinishedCount != 0 {
			t.Fatalf("Leckie progress %d/%d, want none from the sibling profile", leckie.InProgressCount, leckie.FinishedCount)
		}

		q.After = first.Next
		second := list(t, q, f.access(), true)
		if got, want := audiobookGroupKeys(second.Groups), []string{"ben aaronovitch", "zed author"}; !slices.Equal(got, want) || second.HasMore || second.Total != 4 {
			t.Fatalf("second page %v (more %v, total %d), want %v with no more of 4", got, second.HasMore, second.Total, want)
		}
	})

	t.Run("series cover order", func(t *testing.T) {
		result := list(t, AudiobookGroupsQuery{GroupBy: AudiobookGroupBySeries, IncludeTotal: true}, f.access(), true)
		if len(result.Groups) != 1 || result.Groups[0].ItemCount != 5 {
			t.Fatalf("series groups %+v, want The Dark Tower with 5 books", result.Groups)
		}
		if got, want := result.Groups[0].PosterPaths, audiobookCovers("misery", "carrie", "christine-b", "thinner"); !slices.Equal(got, want) {
			t.Fatalf("series covers %v, want %v", got, want)
		}
	})

	t.Run("search prefix", func(t *testing.T) {
		result := list(t, AudiobookGroupsQuery{GroupBy: AudiobookGroupByAuthor, SearchPrefix: "ST", IncludeTotal: true}, f.access(), true)
		if len(result.Groups) != 1 || result.Groups[0].Name != "Stephen King" || result.Total != 1 {
			t.Fatalf("search found %+v (total %d), want only Stephen King", result.Groups, result.Total)
		}
	})

	for _, c := range []struct {
		name    string
		allowed []int
		denied  []int
		want    int
	}{
		{"allowed only another library", []int{f.other}, nil, 1},
		{"another library disabled", nil, []int{f.other}, 1},
		{"allowed the browsed library", []int{f.library}, nil, 2},
	} {
		t.Run("library access/"+c.name, func(t *testing.T) {
			filter := f.access()
			filter.AllowedLibraryIDs, filter.DisabledLibraryIDs = c.allowed, c.denied
			// The view never answers a viewer with library limits.
			result := list(t, AudiobookGroupsQuery{GroupBy: AudiobookGroupByAuthor, SearchPrefix: "ann"}, filter, false)
			if len(result.Groups) != 1 || result.Groups[0].ItemCount != c.want {
				t.Fatalf("Leckie groups %+v, want one with %d books", result.Groups, c.want)
			}
		})
	}
}

// checkKingGroup checks the King group: both spellings of his name, only the
// library's books, the viewer's progress alone, and the first four books with
// a poster by sort title, the two Christine books in content ID order.
func checkKingGroup(t *testing.T, king AudiobookGroup) {
	t.Helper()
	if king.Name != "Stephen King" || king.ItemCount != 7 || king.TotalDurationSeconds != 111111 {
		t.Fatalf("King group %q with %d books and %d s, want Stephen King with 7 and 111111", king.Name, king.ItemCount, king.TotalDurationSeconds)
	}
	if king.InProgressCount != 1 || king.FinishedCount != 1 {
		t.Fatalf("King progress %d in progress, %d finished, want the viewer's 1 and 1", king.InProgressCount, king.FinishedCount)
	}
	if want := audiobookCovers("carrie", "christine-a", "christine-b", "misery"); !slices.Equal(king.PosterPaths, want) {
		t.Fatalf("King covers %v, want %v", king.PosterPaths, want)
	}
}
