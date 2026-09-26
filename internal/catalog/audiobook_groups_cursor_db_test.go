package catalog

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAudiobookGroupsCursorDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	prefix := fmt.Sprintf("audio-cursor-%d", time.Now().UnixNano())
	var lib, uid int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('audiobooks',$1,true) RETURNING id`, prefix).Scan(&lib); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	profile := prefix + "-profile"
	other := prefix + "-other"
	exec(`INSERT INTO user_profiles(id,user_id,name) VALUES($1,$3,'one'),($2,$3,'two')`, profile, other, uid)
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, uid)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM people WHERE name LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, lib)
	}()
	names := []string{" Alpha ", "alpha", "Beta", "Gamma", "Delta", "   ", "Denied"}
	for i, name := range names {
		id := fmt.Sprintf("%s-%d", prefix, i)
		pid := time.Now().UnixNano()
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age,poster_path) VALUES($1,'audiobook',$1,'released','{}',$2,$4,$3)`, id, map[bool]string{true: "R", false: "PG"}[i == 6], map[bool]string{true: "", false: id + ".jpg"}[i == 5], map[bool]int{true: 17, false: 8}[i == 6])
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, lib)
		exec(`INSERT INTO audiobook_series(content_id,series_name) VALUES($1,$2)`, id, name)
		exec(`INSERT INTO people(id,name) VALUES($1,$2)`, pid, prefix+name)
		exec(`INSERT INTO item_people(id,content_id,person_id,kind) VALUES($1,$2,$3,7),($1+1,$2,$3,8)`, pid, id, pid)
		if i < 5 {
			exec(`INSERT INTO audiobook_item_file_stats(media_folder_id,content_id,duration_seconds) VALUES($1,$2,$3)`, lib, id, 100)
		}
		if i == 0 {
			exec(`INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,position_seconds,completed) VALUES($1,$2,$3,50,false),($1,$4,$3,100,true)`, uid, profile, id, other)
		}
	}
	filter := AccessFilter{UserID: uid, ProfileID: profile, AllowedLibraryIDs: []int{lib}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
	for _, axis := range []AudiobookGroupBy{AudiobookGroupBySeries, AudiobookGroupByAuthor, AudiobookGroupByNarrator} {
		for _, sort := range []string{"name", "count", "duration"} {
			t.Run(string(axis)+"/"+sort, func(t *testing.T) {
				q := AudiobookGroupsQuery{LibraryID: lib, GroupBy: axis, Sort: sort, Limit: 50, IncludeTotal: true}
				baseline, err := ListAudiobookGroups(ctx, pool, q, filter)
				if err != nil {
					t.Fatal(err)
				}
				q.CursorPaging = true
				q.Limit = 1
				var got []AudiobookGroup
				for range 20 {
					page, err := ListAudiobookGroups(ctx, pool, q, filter)
					if err != nil {
						t.Fatal(err)
					}
					if page.Total != baseline.Total || !page.TotalExact {
						t.Fatalf("total changed: %+v baseline %+v", page, baseline)
					}
					for _, g := range page.Groups {
						g.GroupKey = ""
						got = append(got, g)
					}
					if !page.HasMore {
						break
					}
					if page.Next == nil {
						t.Fatal("missing key")
					}
					q.After = page.Next
				}
				if !reflect.DeepEqual(got, baseline.Groups) {
					t.Fatalf("cursor differs: %+v baseline %+v", got, baseline.Groups)
				}
				q.IncludeTotal = false
				q.After = nil
				page, err := ListAudiobookGroups(ctx, pool, q, filter)
				if err != nil || page.TotalExact || !page.HasMore {
					t.Fatalf("skip total: %+v %v", page, err)
				}
				q.IncludeTotal = true
				q.After = &AudiobookGroupCursor{GroupKey: "zzzz", Value: -1}
				page, err = ListAudiobookGroups(ctx, pool, q, filter)
				if err != nil || len(page.Groups) != 0 || page.Total != baseline.Total || page.HasMore {
					t.Fatalf("empty tail exact count: %+v %v", page, err)
				}
			})
		}
	}
	t.Run("prefix aggregate profile and empty covers", func(t *testing.T) {
		q := AudiobookGroupsQuery{LibraryID: lib, GroupBy: AudiobookGroupBySeries, Sort: "name", CursorPaging: true, Limit: 2, IncludeTotal: true, SearchPrefix: " AL "}
		page, err := ListAudiobookGroups(ctx, pool, q, filter)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 1 || len(page.Groups) != 1 || page.Groups[0].ItemCount != 2 || page.Groups[0].TotalDurationSeconds != 200 || page.Groups[0].InProgressCount != 1 || page.Groups[0].FinishedCount != 0 || len(page.Groups[0].PosterPaths) != 2 {
			t.Fatalf("aggregate: %+v", page)
		}
		q.SearchPrefix = ""
		page, err = ListAudiobookGroups(ctx, pool, q, filter)
		if err != nil {
			t.Fatal(err)
		}
		if page.Groups[0].GroupKey != "" || len(page.Groups[0].PosterPaths) != 0 || page.Groups[0].TotalDurationSeconds != 0 {
			t.Fatalf("empty key/null duration: %+v", page.Groups[0])
		}
	})
	t.Run("disabled library and unavailable access", func(t *testing.T) {
		q := AudiobookGroupsQuery{LibraryID: lib, GroupBy: AudiobookGroupBySeries, CursorPaging: true, Limit: 2, IncludeTotal: true}
		denied := filter
		denied.DisabledLibraryIDs = []int{lib}
		page, err := ListAudiobookGroups(ctx, pool, q, denied)
		if err != nil || len(page.Groups) != 0 || page.Total != 0 || !page.TotalExact {
			t.Fatalf("disabled library: %+v %v", page, err)
		}
		denied = filter
		denied.AllowedLibraryIDs = []int{}
		page, err = ListAudiobookGroups(ctx, pool, q, denied)
		if err != nil || len(page.Groups) != 0 || page.Total != 0 {
			t.Fatalf("empty access: %+v %v", page, err)
		}
	})
	t.Run("deleted anchor does not shift continuation", func(t *testing.T) {
		q := AudiobookGroupsQuery{LibraryID: lib, GroupBy: AudiobookGroupBySeries, CursorPaging: true, Limit: 2, Sort: "name"}
		first, err := ListAudiobookGroups(ctx, pool, q, filter)
		if err != nil {
			t.Fatal(err)
		}
		q.After = first.Next
		exec(`DELETE FROM audiobook_series WHERE lower(btrim(series_name))='alpha' AND content_id LIKE $1`, prefix+"%")
		page, err := ListAudiobookGroups(ctx, pool, q, filter)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Groups) != 2 || page.Groups[0].GroupKey != "beta" || page.Groups[1].GroupKey != "delta" {
			t.Fatalf("deleted anchor shifted page: %+v", page)
		}
	})
}
