package scanner_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog/filesplit"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// Uses the fully migrated schema. No schema changes or sequence resets occur.
func TestCoreBigintIDsAcrossRepositories(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := time.Now().UnixNano()
	wide := int(3_000_000_000 + suffix%100_000_000)
	var folder int
	if err := pool.QueryRow(ctx, "INSERT INTO media_folders(type,name) VALUES('movies',$1) RETURNING id", fmt.Sprintf("bigint-%d", suffix)).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM media_folders WHERE id=$1 OR id=$2", folder, wide)
	})
	if _, err := pool.Exec(ctx, "INSERT INTO media_folders(id,type,name) VALUES($1,'movies','wide')", wide); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO media_files(id,content_id,media_folder_id,file_path,file_size) VALUES($1,$2,$3,$4,123)", wide, fmt.Sprintf("movie-bigint-%d", suffix), folder, fmt.Sprintf("/bigint/%d.mkv", suffix)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM media_files WHERE id=$1", wide) })
	files, err := scanner.NewFileRepository(pool).GetByIDs(ctx, []int{wide})
	if err != nil || len(files) != 1 || files[0].ID != wide {
		t.Fatalf("GetByIDs: %v, %v", files, err)
	}

	for _, kind := range []string{"movie", "series"} {
		t.Run("split_"+kind, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := tx.Rollback(context.Background()); err != nil {
					t.Errorf("rollback file split: %v", err)
				}
			}()
			from := fmt.Sprintf("movie-bigint-%d", suffix)
			to := fmt.Sprintf("split-bigint-%d", suffix)
			if _, err := tx.Exec(ctx, "INSERT INTO media_items(content_id,type,title) VALUES($1,$3,'source'),($2,$3,'target')", from, to, kind); err != nil {
				t.Fatal(err)
			}
			if _, err := filesplit.Move(ctx, tx, filesplit.Options{FromContentID: from, ToContentID: to, ItemType: kind, Files: []filesplit.File{{ID: wide, ContentID: from, MediaFolderID: folder}}}); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := tx.QueryRow(ctx, "SELECT content_id FROM media_files WHERE id=$1", wide).Scan(&got); err != nil || got != to {
				t.Fatalf("split content_id=%q: %v", got, err)
			}
		})
	}

	bundle := fmt.Sprintf("bigint-%d", suffix)
	for _, scope := range []string{"home", "library"} {
		var library any
		if scope == "library" {
			library = folder
		}
		if _, err := pool.Exec(ctx, `INSERT INTO page_sections(id,scope,library_id,section_type,title,config) VALUES($1,$2,$3,'collection','bigint',jsonb_build_object('generated_source','template_bundle_featured','template_bundle',$4::text,'library_id',$5::bigint))`, bundle+scope, scope, library, bundle, wide); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM page_sections WHERE id=ANY($1)", []string{bundle + "home", bundle + "library"})
	})
	if err := sections.NewRepository(pool).DeleteGeneratedTemplateBundleFeaturedSections(ctx, bundle, []int{wide, folder}); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM page_sections WHERE id=ANY($1)", []string{bundle + "home", bundle + "library"}).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("section deletion remaining=%d: %v", remaining, err)
	}

	// Empty results still require pgx to encode the wide filter before execution.
	if _, err := usercollections.ListServerVisibleByLibrary(ctx, pool, 0, "absent", wide); err != nil {
		t.Fatal(err)
	}
	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	reader := store.(interface {
		ListSettingValuesForResolution(context.Context, userstore.SettingResolutionQuery) ([]userstore.SettingValue, error)
	})
	if _, err := reader.ListSettingValuesForResolution(ctx, userstore.SettingResolutionQuery{Keys: []string{"playback.audio.language"}, ProfileIDs: []string{"absent"}, LibraryIDs: []int{wide}}); err != nil {
		t.Fatal(err)
	}
}
