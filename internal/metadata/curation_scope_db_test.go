package metadata

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// These fixtures exercise only corrected repository sinks, with no providers,
// plugins, scanner, accounts from a deployment, or HTTP curation requests.
func curationFixture(t *testing.T) (*pgxpool.Pool, string, string, int, int) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	source, target := "curation-source-"+t.Name(), "curation-target-"+t.Name()
	var allowed, denied int
	for _, folder := range []*int{&allowed, &denied} {
		if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders(type,name) VALUES('movies','curation fixture') RETURNING id`).Scan(folder); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=ANY($1)`, []string{source, target, target + "-renamed"})
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=ANY($1)`, []int{allowed, denied})
	})
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title,status) VALUES($1,'movie','Source','pending'),($2,'movie','Target','pending')`, source, target); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$3),($2,$3)`, source, target, allowed); err != nil {
		t.Fatal(err)
	}
	return pool, source, target, allowed, denied
}

func curationScope(ctx context.Context, allowed int) context.Context {
	return access.WithMetadataCurationAuthorizer(ctx, func(_ context.Context, libraries []int) error {
		if len(libraries) == 0 || slices.ContainsFunc(libraries, func(id int) bool { return id != allowed }) {
			return access.ErrMetadataCurationScope
		}
		return nil
	})
}

func TestCurationScopeSinksRefuseWithoutWritesPostgres(t *testing.T) {
	for _, sink := range []string{"rebind", "rename", "clear-provisional", "canonical-choice", "canonical-into", "identity-persist"} {
		t.Run(sink, func(t *testing.T) {
			pool, source, target, allowed, denied := curationFixture(t)
			if _, err := pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, target, denied); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(t.Context(), `INSERT INTO media_item_provider_ids(content_id,provider,provider_id,item_type) VALUES($1,'tmdb','4242','movie')`, target); err != nil {
				t.Fatal(err)
			}
			var file int
			if err := pool.QueryRow(t.Context(), `INSERT INTO media_files(content_id,media_folder_id,file_path) VALUES($1,$2,$3) RETURNING id`, source, allowed, "/synthetic/"+t.Name()).Scan(&file); err != nil {
				t.Fatal(err)
			}
			ctx := curationScope(t.Context(), allowed)
			svc := &MetadataService{dbPool: pool}
			var err error
			switch sink {
			case "rebind":
				err = svc.rebindItemOnto(ctx, source, target, true, true)
			case "rename":
				err = svc.renameContentID(ctx, target, target+"-renamed", false)
			case "clear-provisional":
				err = clearProvisionalProviderIDsLocked(ctx, pool, target)
			case "canonical-choice":
				_, err = canonicalizeProviderIDDuplicate(ctx, pool, nil, source, target, true, true)
			case "canonical-into":
				_, err = canonicalizeProviderIDDuplicateInto(ctx, pool, nil, source, target, true, true)
			case "identity-persist":
				err = svc.persistItemAndProviderIDsOnceTx(ctx, catalog.NewItemRepository(pool), catalog.NewProviderIDRepository(pool), &models.MediaItem{ContentID: target, Type: "movie", Title: "Changed"}, nil, true)
			}
			if !errors.Is(err, access.ErrMetadataCurationScope) {
				t.Fatalf("refusal = %v", err)
			}
			var fileOwner, title string
			var items, memberships, providers int
			if err := pool.QueryRow(t.Context(), `SELECT content_id FROM media_files WHERE id=$1`, file).Scan(&fileOwner); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(t.Context(), `SELECT title FROM media_items WHERE content_id=$1`, target).Scan(&title); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM media_items WHERE content_id=ANY($1)`, []string{source, target}).Scan(&items); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM media_item_libraries WHERE content_id=ANY($1)`, []string{source, target}).Scan(&memberships); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM media_item_provider_ids WHERE content_id=$1`, target).Scan(&providers); err != nil {
				t.Fatal(err)
			}
			if fileOwner != source || title != "Target" || items != 2 || memberships != 3 || providers != 1 {
				t.Fatalf("refusal changed state: %s %s %d %d %d", fileOwner, title, items, memberships, providers)
			}
		})
	}
}

func TestCurationScopeRebindPreservesAuthorizedStatePostgres(t *testing.T) {
	for _, admin := range []bool{false, true} {
		name := "delegated"
		if admin {
			name = "acting-admin"
		}
		t.Run(name, func(t *testing.T) {
			pool, source, target, allowed, denied := curationFixture(t)
			ctx := curationScope(t.Context(), allowed)
			if admin {
				ctx = access.WithMetadataCurationAuthorizer(t.Context(), func(context.Context, []int) error { return nil })
				if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, target, denied); err != nil {
					t.Fatal(err)
				}
			}
			var file, user int
			if err := pool.QueryRow(ctx, `INSERT INTO media_files(content_id,media_folder_id,file_path) VALUES($1,$2,$3) RETURNING id`, source, allowed, "/synthetic/"+t.Name()).Scan(&file); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "synthetic-"+t.Name()).Scan(&user); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user) })
			if _, err := pool.Exec(ctx, `INSERT INTO user_favorites(user_id,profile_id,media_item_id) VALUES($1,'synthetic-profile',$2)`, user, source); err != nil {
				t.Fatal(err)
			}
			svc := &MetadataService{dbPool: pool}
			if err := svc.rebindItemOnto(ctx, source, target, true, true); err != nil {
				t.Fatal(err)
			}
			var owner, favorite string
			if err := pool.QueryRow(ctx, `SELECT content_id FROM media_files WHERE id=$1`, file).Scan(&owner); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT media_item_id FROM user_favorites WHERE user_id=$1`, user).Scan(&favorite); err != nil {
				t.Fatal(err)
			}
			if owner != target || favorite != target {
				t.Fatalf("authorized movement: file=%s favorite=%s", owner, favorite)
			}
		})
	}
}

func TestCurationScopeRebindToleratesVanishedSourcePostgres(t *testing.T) {
	for _, admin := range []bool{false, true} {
		name := "delegated"
		if admin {
			name = "acting-admin"
		}
		t.Run(name, func(t *testing.T) {
			pool, source, target, allowed, _ := curationFixture(t)
			ctx := curationScope(t.Context(), allowed)
			if admin {
				ctx = access.WithMetadataCurationAuthorizer(t.Context(), func(context.Context, []int) error { return nil })
			}
			// A scan merged or deleted the source after Process loaded it.
			if _, err := pool.Exec(t.Context(), `DELETE FROM media_items WHERE content_id=$1`, source); err != nil {
				t.Fatal(err)
			}
			svc := &MetadataService{dbPool: pool}
			if err := svc.rebindItemOnto(ctx, source, target, true, true); err != nil {
				t.Fatalf("rebinding a vanished source = %v, want the no-op the caller continues after", err)
			}
			var marked bool
			if err := pool.QueryRow(t.Context(), `SELECT identity_chosen_at IS NOT NULL FROM media_items WHERE content_id=$1`, target).Scan(&marked); err != nil {
				t.Fatal(err)
			}
			if !marked {
				t.Fatal("an Identify whose source vanished did not mark the target, as it does without curation authority")
			}
			if err := svc.rebindItemOnto(ctx, target, source, true, true); !errors.Is(err, catalog.ErrItemNotFound) {
				t.Fatalf("rebinding onto a vanished target = %v, want ErrItemNotFound", err)
			}
			if err := svc.rebindItemOnto(ctx, source, "curation-vanished-"+t.Name(), true, true); !errors.Is(err, catalog.ErrItemNotFound) {
				t.Fatalf("rebinding a vanished source onto a vanished target = %v, want ErrItemNotFound", err)
			}
		})
	}
}

// A target that another writer creates after the guard has read the items is
// visible to the transaction's later statements. The rebind must authorize it
// before moving anything onto it.
func TestCurationScopeRebindAuthorizesLateTargetPostgres(t *testing.T) {
	pool, source, target, allowed, denied := curationFixture(t)
	if _, err := pool.Exec(t.Context(), `DELETE FROM media_items WHERE content_id=$1`, target); err != nil {
		t.Fatal(err)
	}
	var created sync.Once
	ctx := access.WithMetadataCurationAuthorizer(t.Context(), func(_ context.Context, libraries []int) error {
		// The guard calls this after reading the rows it locks.
		created.Do(func() {
			if _, err := pool.Exec(context.Background(), `WITH item AS (INSERT INTO media_items(content_id,type,title,status) VALUES($1,'movie','Target','pending') RETURNING content_id)
				INSERT INTO media_item_libraries(content_id,media_folder_id) SELECT content_id, $2 FROM item`, target, denied); err != nil {
				t.Error(err)
			}
		})
		if slices.ContainsFunc(libraries, func(id int) bool { return id != allowed }) {
			return access.ErrMetadataCurationScope
		}
		return nil
	})
	svc := &MetadataService{dbPool: pool}
	if err := svc.rebindItemOnto(ctx, source, target, true, true); !errors.Is(err, access.ErrMetadataCurationScope) {
		t.Fatalf("rebinding onto a target created in a denied library = %v, want ErrMetadataCurationScope", err)
	}
	var sourceLibraries int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM media_item_libraries WHERE content_id=$1 AND media_folder_id=$2`, source, allowed).Scan(&sourceLibraries); err != nil {
		t.Fatal(err)
	}
	if sourceLibraries != 1 {
		t.Fatal("the refused rebind moved the source's library membership")
	}
}

func TestCurationScopeClearToleratesVanishedOwnerPostgres(t *testing.T) {
	pool, _, _, allowed, _ := curationFixture(t)
	// The provisional owner was merged or deleted after the caller found it.
	if err := clearProvisionalProviderIDsLocked(curationScope(t.Context(), allowed), pool, "curation-vanished-"+t.Name()); err != nil {
		t.Fatalf("clearing a vanished owner = %v, want the no-op the caller retries after", err)
	}
}

// A split holds its selected files, then locks their parent item. Rebind and
// rename update the source's files under curation authority, so they must take
// those file locks before the parent lock, or the two deadlock.
func TestCurationScopeLocksFilesBeforeParentPostgres(t *testing.T) {
	for _, sink := range []string{"rebind", "rename"} {
		t.Run(sink, func(t *testing.T) {
			pool, source, target, allowed, _ := curationFixture(t)
			var file int
			if err := pool.QueryRow(t.Context(), `INSERT INTO media_files(content_id,media_folder_id,file_path) VALUES($1,$2,$3) RETURNING id`, source, allowed, "/synthetic/"+t.Name()).Scan(&file); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE id=$1`, file)
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, source+"-renamed")
			})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			split, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = split.Rollback(context.Background()) }()
			var splitPID int
			if err := split.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&splitPID); err != nil {
				t.Fatal(err)
			}
			if _, err := split.Exec(ctx, `SELECT 1 FROM media_files WHERE id = $1 FOR UPDATE`, file); err != nil {
				t.Fatal(err)
			}
			svc := &MetadataService{dbPool: pool}
			done := make(chan error, 1)
			go func() {
				ctx := curationScope(ctx, allowed)
				if sink == "rebind" {
					done <- svc.rebindItemOnto(ctx, source, target, true, true)
				} else {
					done <- svc.renameContentID(ctx, source, source+"-renamed", false)
				}
			}()
			waitForBlockedBy(t, ctx, pool, splitPID)
			if _, err := split.Exec(ctx, `SELECT 1 FROM media_items WHERE content_id = $1 FOR UPDATE`, source); err != nil {
				t.Fatalf("split's parent lock while the %s waits: %v", sink, err)
			}
			if err := split.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("%s after the split committed: %v", sink, err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

// waitForBlockedBy returns once another backend waits on a lock held by pid.
func waitForBlockedBy(t *testing.T, ctx context.Context, pool *pgxpool.Pool, pid int) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}
