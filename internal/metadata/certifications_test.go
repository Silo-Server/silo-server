package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

type certificationProviderStub struct {
	calls int
	err   error
}

func (p *certificationProviderStub) GetCertifications(context.Context, string, int) (map[string]string, error) {
	p.calls++
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
}
