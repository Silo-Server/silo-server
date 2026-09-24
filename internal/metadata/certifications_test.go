package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

type certificationProviderStub struct {
	calls       int
	err         error
	duringFetch func()
}

func (p *certificationProviderStub) GetCertifications(context.Context, string, int) (map[string]string, error) {
	p.calls++
	if p.duringFetch != nil {
		p.duringFetch()
	}
	return map[string]string{"AU": "M", "US": "PG-13"}, p.err
}

func TestRefreshCertificationsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,certification_country) VALUES('movies','Certification refresh','AU') RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	item := &models.MediaItem{ContentID: fmt.Sprintf("movie:cert-refresh-%d", time.Now().UnixNano()), Type: "movie", TmdbID: "123"}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id=$1`, item.ContentID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, folder)
	}()
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,tmdb_id) VALUES($1,'movie','Certification test','123')`, item.ContentID); err != nil {
		t.Fatal(err)
	}
	provider := &certificationProviderStub{}
	svc := &MetadataService{dbPool: pool, certificationProvider: provider}
	if err := svc.refreshCertifications(ctx, item, folder, []MetadataField{FieldContentRating}); err != nil || provider.calls != 0 {
		t.Fatalf("locked rating fetched: %v", err)
	}
	if err := svc.refreshCertifications(ctx, item, folder, nil); err != nil {
		t.Fatal(err)
	}
	provider.err = errors.New("provider unavailable")
	if err := svc.refreshCertifications(ctx, item, folder, nil); err == nil {
		t.Fatal("provider failure swallowed")
	}
	var rating string
	if err := pool.QueryRow(ctx, `SELECT ratings->>'AU' FROM media_item_certifications WHERE content_id=$1`, item.ContentID).Scan(&rating); err != nil || rating != "M" {
		t.Fatalf("snapshot lost: %s %v", rating, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_folders SET certification_country='US' WHERE id=$1`, folder); err != nil {
		t.Fatal(err)
	}
	if err := svc.refreshCertifications(ctx, item, folder, nil); err != nil || provider.calls != 2 {
		t.Fatalf("US-only library fetched: %v", err)
	}
	// A manual certification-only refresh also works before switching country.
	svc.itemRepo = catalog.NewItemRepository(pool)
	provider.err = nil
	if err := svc.RefreshItemCertifications(ctx, item.ContentID); err != nil || provider.calls != 3 {
		t.Fatalf("targeted refresh: %v calls=%d", err, provider.calls)
	}
	var title, tmdbID string
	if err := pool.QueryRow(ctx, `SELECT title,tmdb_id FROM media_items WHERE content_id=$1`, item.ContentID).Scan(&title, &tmdbID); err != nil || title != "Certification test" || tmdbID != "123" {
		t.Fatalf("other metadata changed: %s %s %v", title, tmdbID, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_items SET locked_fields=ARRAY[9] WHERE content_id=$1`, item.ContentID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RefreshItemCertifications(ctx, item.ContentID); err == nil || provider.calls != 3 {
		t.Fatalf("locked targeted refresh: %v", err)
	}

}

func TestCertificationRefreshRejectsChangedItemDB(t *testing.T) {
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
	for _, change := range []struct{ name, query string }{
		{"deleted", `DELETE FROM media_items WHERE content_id=$1`},
		{"reidentified", `UPDATE media_items SET tmdb_id='456' WHERE content_id=$1`},
		{"locked", `UPDATE media_items SET locked_fields=ARRAY[9] WHERE content_id=$1`},
	} {
		t.Run(change.name, func(t *testing.T) {
			id := fmt.Sprintf("movie:cert-race-%d", time.Now().UnixNano())
			if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,tmdb_id) VALUES($1,'movie','Certification race','123')`, id); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id=$1`, id) }()
			provider := &certificationProviderStub{duringFetch: func() {
				if _, err := pool.Exec(ctx, change.query, id); err != nil {
					t.Fatal(err)
				}
			}}
			svc := &MetadataService{dbPool: pool, itemRepo: catalog.NewItemRepository(pool), certificationProvider: provider}
			if err := svc.RefreshItemCertifications(ctx, id); err == nil {
				t.Fatal("changed item reported a successful refresh")
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM media_item_certifications WHERE content_id=$1`, id).Scan(&count); err != nil || count != 0 {
				t.Fatalf("stale snapshot stored: %d %v", count, err)
			}
		})
	}
}

func TestCertificationFailureDoesNotAbortMetadataDB(t *testing.T) {
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
	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,certification_country) VALUES('movies','Certification outage','AU') RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, folder) }()
	h := newTestHarness()
	const id = "movie:certification-outage"
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{ContentID: id, Type: "movie", Title: "Original", TmdbID: "123", Status: "matched"}); err != nil {
		t.Fatal(err)
	}
	provider := &certificationProviderStub{err: errors.New("certification provider unavailable")}
	h.service.dbPool = pool
	h.service.certificationProvider = provider
	enqueuer := &recordingImageCacheJobEnqueuer{}
	h.service.SetAutoCacheImages(true)
	h.service.SetImageCacheJobEnqueuer(enqueuer)
	result, err := h.service.mergeAndPersist(ctx, ProcessRequest{ContentID: id, FolderID: strconv.Itoa(folder), Mode: ModeManualRefresh, Language: "en"}, &MetadataResult{HasMetadata: true, Title: "Updated", ProviderIDs: map[string]string{"tmdb": "123"}}, []RemoteImage{{Type: ImagePoster, URL: "tmdb://certification-outage.jpg", ProviderID: "tmdb", Language: "en"}}, nil, nil, "movie")
	if err != nil || result == nil || !result.Updated || provider.calls != 1 {
		t.Fatalf("normal refresh interrupted: %+v %v calls=%d", result, err, provider.calls)
	}
	if len(enqueuer.inputs) == 0 {
		t.Fatal("artwork processing did not continue after certification failure")
	}
}
