package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCatalogPersonalCursorDB(t *testing.T) {
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
	prefix := fmt.Sprintf("personal-cursor-%d", time.Now().UnixNano())
	var uid, lib int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('tv',$1,true) RETURNING id`, prefix).Scan(&lib); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, uid)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, lib)
	}()
	p1, p2 := prefix+"-profile1", prefix+"-profile2"
	exec(`INSERT INTO user_profiles(id,user_id,name) VALUES($1,$3,'one'),($2,$3,'two')`, p1, p2, uid)
	var ids []string
	for i := range 5 {
		id := fmt.Sprintf("%s-%d", prefix, i)
		ids = append(ids, id)
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age) VALUES($1,'movie',$2,'released','{}',$3,$4)`, id, fmt.Sprintf("Title %d", 4-i), map[bool]string{true: "R", false: "PG"}[i == 4], map[bool]int{true: 17, false: 8}[i == 4])
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, lib)
		exec(`INSERT INTO user_favorites(user_id,profile_id,media_item_id,added_at) VALUES($1,$2,$3,'2025-01-01'::timestamptz)`, uid, p1, id)
		exec(`INSERT INTO user_watchlist(user_id,profile_id,media_item_id,added_at) VALUES($1,$2,$3,'2025-01-01'::timestamptz)`, uid, p1, id)
		exec(`INSERT INTO user_watch_history(id,user_id,profile_id,media_item_id,watched_at) VALUES($1,$2,$3,$4,'2025-01-01'::timestamptz)`, id, uid, p1, id)
	}
	exec(`INSERT INTO user_favorites(user_id,profile_id,media_item_id) VALUES($1,$2,$3)`, uid, p2, ids[3])
	provider := pgstore.NewPostgresProvider(pool)
	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).WithUserStoreProvider(provider)
	access := AccessFilter{UserID: uid, ProfileID: p1, AllowedLibraryIDs: []int{lib}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
	walk := func(req CatalogRequest, viewer AccessFilter) []string {
		t.Helper()
		var result []string
		for range 10 {
			page, err := resolver.Resolve(ctx, req, viewer)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range page.Items {
				result = append(result, item.ContentID)
			}
			if !page.HasMore {
				return result
			}
			if page.Next == nil {
				t.Fatal("missing tuple")
			}
			req.After = page.Next
			req.SnapshotAt = &page.SnapshotAt
			req.ResolvedSort = &page.EffectiveSort
		}
		t.Fatal("cursor never ended")
		return nil
	}
	for _, source := range []CatalogSource{CatalogSourceFavorites, CatalogSourceWatchlist, CatalogSourceHistory} {
		t.Run(string(source), func(t *testing.T) {
			req := CatalogRequest{Source: source, CursorPaging: true, UseSourceOrder: true, Limit: 2}
			got := walk(req, access)
			if !reflect.DeepEqual(got, ids[:4]) {
				t.Fatalf("source tie/access got %v want %v", got, ids[:4])
			}
			req.Seek = new(2)
			page, err := resolver.Resolve(ctx, req, access)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 2 || page.Items[0].ContentID != ids[2] {
				t.Fatalf("jump: %+v", page)
			}
		})
	}
	t.Run("profile and saved sort", func(t *testing.T) {
		req := CatalogRequest{Source: CatalogSourceFavorites, CursorPaging: true, UseSourceOrder: true, Limit: 2}
		other := access
		other.ProfileID = p2
		if got := walk(req, other); !reflect.DeepEqual(got, []string{ids[3]}) {
			t.Fatalf("profile leak: %v", got)
		}
		store, err := provider.ForUser(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetCollectionSortPreference(ctx, userstore.CollectionSortPreference{ProfileID: p1, CollectionKind: "favorites", CollectionID: userstore.PersonalSortPreferenceCollectionID, SortField: "title", SortOrder: "asc"}); err != nil {
			t.Fatal(err)
		}
		want := []string{ids[3], ids[2], ids[1], ids[0]}
		if got := walk(req, access); !reflect.DeepEqual(got, want) {
			t.Fatalf("saved sort: %v", got)
		}
		req.Query.Sort = QuerySort{Field: "added_at", Order: "asc"}
		req.UseSourceOrder = true
		if got := walk(req, access); !reflect.DeepEqual(got, ids[:4]) {
			t.Fatalf("explicit sort: %v", got)
		}
	})
	series := prefix + "-series"
	exec(`INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age) VALUES($1,'series','Series','released','{}','PG',8)`, series)
	exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, series, lib)
	for i := range 2 {
		ep := fmt.Sprintf("%s-ep%d", prefix, i)
		exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,$3,'Episode')`, ep, series, i+1)
		exec(`INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES($1,$2)`, ep, lib)
		exec(`INSERT INTO user_watch_history(id,user_id,profile_id,media_item_id,watched_at) VALUES($1,$2,$3,$4,'2025-02-01'::timestamptz)`, ep, uid, p1, ep)
		exec(`INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,completed) VALUES($1,$2,$3,true)`, uid, p1, ep)
	}
	t.Run("explicit history date viewed cursor uses snapshot watch events", func(t *testing.T) {
		// Incomplete movie watches and episode-only series history must sort by
		// their latest event, rather than by completed progress on the display ID.
		exec(`INSERT INTO user_watch_history(id,user_id,profile_id,media_item_id,watched_at,completed) VALUES
			($1 || '-p2',$5,$6,$1,'2025-04-04'::timestamptz,false),
			($2 || '-p2',$5,$6,$2,'2025-04-01'::timestamptz,true),
			($3 || '-p2',$5,$6,$3,'2025-04-02'::timestamptz,false),
			($4 || '-p2',$5,$6,$4,'2025-04-03'::timestamptz,false)`,
			ids[0], ids[1], prefix+"-ep0", prefix+"-ep1", uid, p2)
		viewer := access
		viewer.ProfileID = p2
		snapshot := time.Now().UTC()
		for _, scope := range []string{"", "episode"} {
			for _, order := range []string{"desc", "asc"} {
				t.Run(scope+"/"+order, func(t *testing.T) {
					req, err := ParseCatalogRequest(url.Values{"source": {"history"}, "sort": {"date_viewed"}, "order": {order}, "type": {scope}, "limit": {"1"}, "snapshot": {snapshot.Format(time.RFC3339Nano)}})
					if err != nil {
						t.Fatal(err)
					}
					req.CursorPaging = true
					want := []string{ids[0], series, ids[1]}
					if scope == "episode" {
						want = []string{prefix + "-ep1", prefix + "-ep0"}
					}
					if order == "asc" {
						slices.Reverse(want)
					}
					first, err := resolver.Resolve(ctx, req, viewer)
					if err != nil {
						t.Fatal(err)
					}
					if len(first.Items) != 1 || first.Next == nil {
						t.Fatalf("first page = %+v, want %s with continuation", first, want[0])
					}
					if first.Items[0].ContentID != want[0] {
						t.Fatalf("first item = %s, want %s", first.Items[0].ContentID, want[0])
					}
					// A completed event arriving after the snapshot must not move
					// an item across the continuation or explicit seek boundary.
					exec(`INSERT INTO user_watch_history(id,user_id,profile_id,media_item_id,watched_at,completed) VALUES($1,$2,$3,$4,$5,true)`, prefix+scope+order, uid, p2, ids[1], snapshot.Add(time.Hour))
					exec(`INSERT INTO user_watch_history(id,user_id,profile_id,media_item_id,watched_at,completed) VALUES($1,$2,$3,$4,$5,true)`, prefix+scope+order+"-episode", uid, p2, prefix+"-ep0", snapshot.Add(time.Hour))
					req.After, req.SnapshotAt = first.Next, &first.SnapshotAt
					if got := walk(req, viewer); !slices.Equal(got, want[1:]) {
						t.Fatalf("continuation = %v, want %v", got, want[1:])
					}
					req.After, req.Seek = nil, new(1)
					jump, err := resolver.Resolve(ctx, req, viewer)
					if err != nil {
						t.Fatal(err)
					}
					if len(jump.Items) != 1 || jump.Items[0].ContentID != want[1] {
						t.Fatalf("seek = %+v, want %s", jump, want[1])
					}
				})
			}
		}
	})
	t.Run("history episode collapse and hidden events", func(t *testing.T) {
		req := CatalogRequest{Source: CatalogSourceHistory, CursorPaging: true, UseSourceOrder: true, Limit: 1}
		got := walk(req, access)
		want := append([]string{series}, ids[:4]...)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("collapsed: %v", got)
		}
		req.Query.MediaScope = "episode"
		got = walk(req, access)
		want = []string{prefix + "-ep0", prefix + "-ep1"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("episodes: %v", got)
		}
		exec(`INSERT INTO user_history_hidden_items(user_id,profile_id,media_item_id,hidden_before) VALUES($1,$2,$3,'2025-03-01'::timestamptz)`, uid, p1, prefix+"-ep0")
		if got = walk(req, access); !reflect.DeepEqual(got, want[1:]) {
			t.Fatalf("hidden: %v", got)
		}
	})
	t.Run("watchlist completed series resurfaces", func(t *testing.T) {
		exec(`INSERT INTO user_watchlist(user_id,profile_id,media_item_id) VALUES($1,$2,$3)`, uid, p1, series)
		req := CatalogRequest{Source: CatalogSourceWatchlist, CursorPaging: true, UseSourceOrder: true, Limit: 2}
		if got := walk(req, access); !reflect.DeepEqual(got, ids[:4]) {
			t.Fatalf("completed series visible: %v", got)
		}
		exec(`UPDATE user_history_hidden_items SET hidden_before=now()+interval '1 day' WHERE user_id=$1 AND profile_id=$2 AND media_item_id=$3`, uid, p1, prefix+"-ep0")
		if got := walk(req, access); len(got) != 5 || got[0] != series {
			t.Fatalf("hidden progress treated as completed: %v", got)
		}
		exec(`UPDATE user_history_hidden_items SET hidden_before='2025-03-01'::timestamptz WHERE user_id=$1 AND profile_id=$2 AND media_item_id=$3`, uid, p1, prefix+"-ep0")
		exec(`UPDATE user_watch_progress SET completed=false WHERE user_id=$1 AND profile_id=$2 AND media_item_id=$3`, uid, p1, prefix+"-ep1")
		if got := walk(req, access); len(got) != 5 || got[0] != series {
			t.Fatalf("unwatched series absent: %v", got)
		}
	})
	t.Run("person deduplicates credits and applies filters", func(t *testing.T) {
		var person int64
		if err := pool.QueryRow(ctx, `INSERT INTO people(id,name) VALUES($1,$2) RETURNING id`, time.Now().UnixNano(), prefix).Scan(&person); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM people WHERE id=$1`, person) }()
		exec(`INSERT INTO item_people(id,content_id,person_id,kind) VALUES($4,$1,$3,1),($4+1,$1,$3,2),($4+2,$2,$3,1)`, ids[0], ids[1], person, time.Now().UnixNano())
		req := CatalogRequest{Source: CatalogSourcePerson, PersonID: person, CursorPaging: true, Limit: 1, Query: QueryDefinition{Sort: QuerySort{Field: "title", Order: "asc"}}}
		if got := walk(req, access); !reflect.DeepEqual(got, []string{ids[1], ids[0]}) {
			t.Fatalf("person page: %v", got)
		}
	})
	t.Run("list exceeds legacy candidate ceiling", func(t *testing.T) {
		bulk := prefix + "-bulk-"
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age) SELECT $1 || lpad(n::text,5,'0'),'movie','Bulk','released','{}','PG',8 FROM generate_series(1,10005) n`, bulk)
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) SELECT content_id,$2 FROM media_items WHERE content_id LIKE $1`, bulk+"%", lib)
		exec(`INSERT INTO user_favorites(user_id,profile_id,media_item_id,added_at) SELECT $1,$2,content_id,'2024-01-01'::timestamptz FROM media_items WHERE content_id LIKE $3`, uid, p1, bulk+"%")
		exec(`ANALYZE media_items`)
		exec(`ANALYZE media_item_libraries`)
		exec(`ANALYZE user_favorites`)
		req := CatalogRequest{Source: CatalogSourceFavorites, CursorPaging: true, UseSourceOrder: true, Limit: 2, Seek: new(10004), Query: QueryDefinition{Sort: QuerySort{Field: "added_at", Order: "desc"}}}
		page, err := resolver.Resolve(ctx, req, access)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 10009 || len(page.Items) != 2 || page.Items[0].ContentID != bulk+"10001" || !page.HasMore {
			t.Fatalf("large list truncated: %+v", page)
		}
	})
	t.Run("non-PG provider fails explicitly", func(t *testing.T) {
		resolver.storeProvider = personalCursorUnsupportedProvider{}
		_, err := resolver.Resolve(ctx, CatalogRequest{Source: CatalogSourceFavorites, CursorPaging: true, Limit: 2}, access)
		if !errors.Is(err, ErrCatalogStorageUnsupported) {
			t.Fatalf("unsupported: %v", err)
		}
	})
}

type personalCursorUnsupportedProvider struct{ userstore.UserStoreProvider }

func (personalCursorUnsupportedProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return personalCursorUnsupportedStore{}, nil
}

type personalCursorUnsupportedStore struct{ userstore.UserStore }
