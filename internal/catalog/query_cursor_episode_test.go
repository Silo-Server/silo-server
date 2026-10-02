package catalog

import (
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestQueryCursorPostgresEpisodeParity(t *testing.T) {
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
	series := fmt.Sprintf("cursor-episode-%d", time.Now().UnixNano())
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series','cursor episodes',true) RETURNING id`).Scan(&library); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id=$1`, series)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, library)
	}()
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,status,genres) VALUES($1,'series','Episode Cursor Series','released','{Comedy}')`, series); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, series, library); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		id := fmt.Sprintf("%s-%d", series, i)
		if _, err := pool.Exec(ctx, `INSERT INTO episodes(content_id,series_id,season_number,episode_number,title,air_date) VALUES($1,$2,1,$3,'Tied title',CASE WHEN $3<4 THEN '2025-01-01'::date ELSE NULL END)`, id, series, i+1); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO episode_libraries(episode_id,media_folder_id,first_seen_at) VALUES($1,$2,'2025-01-01'::timestamptz)`, id, library); err != nil {
			t.Fatal(err)
		}
	}

	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, series).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) }()
	profile := "10000000-0000-4000-8000-000000000005"
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'episodes')`, profile, userID); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		id := fmt.Sprintf("%s-%d", series, i)
		if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,position_seconds,duration_seconds,completed) VALUES($1,$2,$3,$4,100,false)`, userID, profile, id, 25*(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_history_hidden_items(user_id,profile_id,media_item_id,hidden_before) VALUES($1,$2,$3,now()+interval '1 second')`, userID, profile, series+"-2"); err != nil {
		t.Fatal(err)
	}
	executor := QueryExecutor{Pool: pool, Scope: "episode"}
	access := AccessFilter{AllowedLibraryIDs: []int{library}, UserID: userID, ProfileID: profile}
	for _, field := range []string{"title", "added_at", "release_date", "year", "rating_imdb", "runtime", "progress", "date_viewed", "plays"} {
		for _, order := range []string{"asc", "desc"} {
			t.Run(field+order, func(t *testing.T) {
				def := QueryDefinition{MediaScope: "episode", LibraryIDs: []int{library}, Sort: QuerySort{Field: field, Order: order}, Groups: []QueryGroup{{Rules: []QueryRule{{Field: "genre", Op: "is", Value: "Comedy"}}}}}
				optimized, _, _, err := executor.PreviewPage(ctx, def, access, 100, 0, false)
				if err != nil {
					t.Fatal(err)
				}
				want := []string{}
				for _, it := range optimized {
					want = append(want, it.ContentID)
				}
				if len(want) != 5 {
					t.Fatalf("fixture returned %d episodes", len(want))
				}
				got := []string{}
				var cursor *QueryCursor
				for range 5 {
					page, err := executor.PreviewCursorPage(ctx, def, access, 2, cursor, true)
					if err != nil {
						t.Fatal(err)
					}
					if page.Total != 5 {
						t.Fatalf("wrong total %d", page.Total)
					}
					for _, it := range page.Items {
						got = append(got, it.ContentID)
					}
					if !page.HasMore {
						break
					}
					cursor = page.Next
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("cursor %v differs optimized %v", got, want)
				}
			})
		}
	}
	denied := access
	denied.DisabledLibraryIDs = []int{library}
	page, err := executor.PreviewCursorPage(ctx, QueryDefinition{MediaScope: "episode"}, denied, 2, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatal("disabled episode library visible")
	}
}
