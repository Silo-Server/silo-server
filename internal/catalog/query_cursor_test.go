package catalog

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestQueryCursorSortTerms(t *testing.T) {
	for field := range querySortDefs {
		for _, order := range []string{"asc", "desc"} {
			t.Run(field+order, func(t *testing.T) {
				plan, err := NewQueryBuilder("mi").WithUserScope(1, "p").BuildSortPlan(QuerySort{Field: field, Order: order})
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.terms) < 2 || plan.terms[len(plan.terms)-1].expression != "mi.content_id" {
					t.Fatalf("missing identity tuple: %#v", plan.terms)
				}
				parts := []string{}
				for _, term := range plan.terms {
					dir := "ASC"
					if term.descending {
						dir = "DESC"
					}
					p := term.expression + " " + dir
					if term.nullsLast && term.descending {
						p += " NULLS LAST"
					}
					parts = append(parts, p)
				}
				// Explicit ASC NULLS LAST is equivalent to PostgreSQL's ASC default.
				actual := strings.ReplaceAll(plan.OrderBy, " ASC NULLS LAST", " ASC")
				if actual != "ORDER BY "+strings.Join(parts, ", ") {
					t.Fatalf("tuple differs: %s vs %s", actual, strings.Join(parts, ", "))
				}
			})
		}
	}
}

func TestQueryCursorPostgresBoundaries(t *testing.T) {
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
	prefix := fmt.Sprintf("cursor-%d-", time.Now().UnixNano())
	ids := []string{}
	for i := range 7 {
		id := fmt.Sprintf("%s%d", prefix, i)
		ids = append(ids, id)
		var rating any
		if i < 5 {
			rating = float64(i / 2)
		}
		_, err = pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,rating_imdb,genres,status,last_air_date_at) VALUES($1,'movie',$2,$3,'{}','released', CASE WHEN $3::double precision IS NULL THEN NULL ELSE DATE '2020-01-01' + $3::double precision::int END)`, id, fmt.Sprintf("Title %d", i/2), rating)
		if err != nil {
			t.Fatal(err)
		}
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%") }()
	access := AccessFilter{AllowedContentIDs: ids}
	executor := QueryExecutor{Pool: pool}
	for _, field := range []string{"title", "rating_imdb", "added_at", "year", "runtime", "content_rating", "last_air_date"} {
		for _, order := range []string{"asc", "desc"} {
			t.Run(field+order, func(t *testing.T) {
				def := QueryDefinition{Sort: QuerySort{Field: field, Order: order}}
				baseline, _, _, err := executor.PreviewPage(ctx, def, access, 100, 0, false)
				if err != nil {
					t.Fatal(err)
				}
				want := []string{}
				for _, it := range baseline {
					want = append(want, it.ContentID)
				}
				got := []string{}
				var cursor *QueryCursor
				for range 10 {
					page, err := executor.PreviewCursorPage(ctx, def, access, 2, cursor, true)
					if err != nil {
						t.Fatal(err)
					}
					if page.Total != 7 || !page.TotalExact {
						t.Fatalf("total: %#v", page)
					}
					for _, it := range page.Items {
						got = append(got, it.ContentID)
					}
					if !page.HasMore {
						break
					}
					if page.Next == nil {
						t.Fatal("missing continuation")
					}
					cursor = page.Next
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("got %v want %v", got, want)
				}
			})
		}
	}

	t.Run("cap and jump", func(t *testing.T) {
		def := QueryDefinition{Sort: QuerySort{Field: "title", Order: "asc"}, Limit: new(5)}
		first, err := executor.PreviewCursorPage(ctx, def, access, 2, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		second, err := executor.PreviewCursorPage(ctx, def, access, 2, first.Next, false)
		if err != nil {
			t.Fatal(err)
		}
		last, err := executor.PreviewCursorPage(ctx, def, access, 2, second.Next, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(last.Items) != 1 || last.HasMore || last.Next != nil {
			t.Fatalf("cap not enforced: %#v", last)
		}
		boundary, err := executor.SeekCursor(ctx, def, access, 4)
		if err != nil {
			t.Fatal(err)
		}
		jumped, err := executor.PreviewCursorPage(ctx, def, access, 2, boundary, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(jumped.Items) != 1 || jumped.Items[0].ContentID != last.Items[0].ContentID {
			t.Fatalf("jump mismatch: %#v", jumped)
		}
	})
	t.Run("profile and disabled library", func(t *testing.T) {
		var userID, folderID int
		if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) }()
		p1 := "10000000-0000-4000-8000-000000000001"
		p2 := "10000000-0000-4000-8000-000000000002"
		if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id,user_id,name) VALUES($1,$3,'one'),($2,$3,'two')`, p1, p2, userID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO user_favorites(user_id,profile_id,media_item_id) VALUES($1,$2,$3),($1,$4,$5)`, userID, p1, ids[0], p2, ids[1]); err != nil {
			t.Fatal(err)
		}
		scoped := access
		scoped.UserID = userID
		scoped.ProfileID = p1
		def := QueryDefinition{Groups: []QueryGroup{{Rules: []QueryRule{{Field: "favorited", Op: "is", Value: true}}}}, Sort: QuerySort{Field: "title"}}
		page, err := executor.PreviewCursorPage(ctx, def, scoped, 2, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].ContentID != ids[0] {
			t.Fatalf("profile isolation failed: %#v", page)
		}

		if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,position_seconds,duration_seconds,completed) VALUES($1,$2,$3,50,100,false),($1,$2,$4,25,100,false),($1,$5,$6,99,100,false)`, userID, p1, ids[0], ids[2], p2, ids[1]); err != nil {
			t.Fatal(err)
		}
		progressDef := QueryDefinition{Groups: []QueryGroup{{Rules: []QueryRule{{Field: "in_progress", Op: "is", Value: true}}}}, Sort: QuerySort{Field: "progress", Order: "desc"}}
		progressPage, err := executor.PreviewCursorPage(ctx, progressDef, scoped, 1, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(progressPage.Items) != 1 || progressPage.Items[0].ContentID != ids[0] || !progressPage.HasMore {
			t.Fatalf("wrong personalized order: %#v", progressPage)
		}
		progressNext, err := executor.PreviewCursorPage(ctx, progressDef, scoped, 1, progressPage.Next, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(progressNext.Items) != 1 || progressNext.Items[0].ContentID != ids[2] || progressNext.HasMore {
			t.Fatalf("wrong progress continuation: %#v", progressNext)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO user_history_hidden_items(user_id,profile_id,media_item_id,hidden_before) VALUES($1,$2,$3,now()+interval '1 second')`, userID, p1, ids[0]); err != nil {
			t.Fatal(err)
		}
		hidden, err := executor.PreviewCursorPage(ctx, progressDef, scoped, 2, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(hidden.Items) != 1 || hidden.Items[0].ContentID != ids[2] {
			t.Fatalf("hidden history visible: %#v", hidden)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies','cursor',true) RETURNING id`).Scan(&folderID); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, folderID) }()
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, ids[0], folderID); err != nil {
			t.Fatal(err)
		}
		scoped.DisabledLibraryIDs = []int{folderID}
		page, err = executor.PreviewCursorPage(ctx, def, scoped, 2, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 0 {
			t.Fatal("disabled library visible")
		}
	})
	def := QueryDefinition{Sort: QuerySort{Field: "title", Order: "asc"}}
	first, err := executor.PreviewCursorPage(ctx, def, access, 2, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	deleted := first.Items[1].ContentID
	if _, err = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id=$1`, deleted); err != nil {
		t.Fatal(err)
	}
	inserted := prefix + "new"
	if _, err = pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,genres,status) VALUES($1,'movie','AAA','{}','released')`, inserted); err != nil {
		t.Fatal(err)
	}
	access.AllowedContentIDs = append(access.AllowedContentIDs, inserted)
	next, err := executor.PreviewCursorPage(ctx, def, access, 200, first.Next, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Items) != 5 {
		t.Fatalf("deleted boundary/lower insertion shifted continuation: %d", len(next.Items))
	}
	for _, it := range next.Items {
		if it.ContentID == inserted || it.ContentID == first.Items[0].ContentID {
			t.Fatal("repeated earlier tuple")
		}
	}
}
