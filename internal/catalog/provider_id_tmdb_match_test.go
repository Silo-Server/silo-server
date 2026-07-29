package catalog

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Real values seen rejected in production: media_items.tmdb_id held TMDB's
// "id-slug" URL form while the presence lookup attached the bare numeric id, so
// the backfill failed and the client kept offering to request titles already in
// the library.
func TestSameTMDBIDAcceptsTheSlugURLForm(t *testing.T) {
	cases := []struct {
		stored string
		want   string
		same   bool
	}{
		{"1931-disney-s-adventures-of-the-gummi-bears", "1931", true},
		{"206709-belascoaran-pi", "206709", true},
		{"1931", "1931", true},
		{" 1931 ", "1931", true},
		// Different titles must still conflict — the slug is decoration, the
		// number is the identity.
		{"1931-disney-s-adventures-of-the-gummi-bears", "19310", false},
		{"206709-belascoaran-pi", "1931", false},
		// Not an id at all: no digits, or digits not followed by a separator.
		{"tt0111161", "1931", false},
		{"12ab", "12", false},
	}
	for _, tc := range cases {
		if got := sameTMDBID(tc.stored, tc.want); got != tc.same {
			t.Errorf("sameTMDBID(%q, %q) = %v, want %v", tc.stored, tc.want, got, tc.same)
		}
	}
}

func TestNormalizeTMDBIDLeavesNonIdentifiersAlone(t *testing.T) {
	if got := normalizeTMDBID("tt0111161"); got != "tt0111161" {
		t.Errorf("normalizeTMDBID mangled a non-numeric id: %q", got)
	}
	if got := normalizeTMDBID(""); got != "" {
		t.Errorf("normalizeTMDBID(%q) = %q", "", got)
	}
}

func TestAttachTMDBIDRejectsEquivalentSlugOwner(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("SILO_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	// A run of nines guards the lexical range bound used by the owner lookup:
	// incrementing the numeric id would produce a shorter, invalid upper bound.
	tmdbID := 999_999_999
	ownerContentID := fmt.Sprintf("test-tmdb-slug-owner-%d", suffix)
	targetContentID := fmt.Sprintf("test-tmdb-slug-target-%d", suffix)
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, tmdb_id)
		VALUES ($1, 'series', 'TMDB slug owner test', $3),
		       ($2, 'series', 'TMDB slug target test', '')
	`, ownerContentID, targetContentID, fmt.Sprintf("%d-some-title", tmdbID)); err != nil {
		t.Fatalf("insert test media items: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{ownerContentID, targetContentID}); err != nil {
			t.Errorf("clean up test media items: %v", err)
		}
	})

	err = NewProviderIDRepository(pool).AttachTMDBID(ctx, targetContentID, "series", tmdbID)
	if err == nil || !strings.Contains(err.Error(), "already belongs to content_id") {
		t.Fatalf("AttachTMDBID() error = %v, want existing-owner conflict", err)
	}

	var targetTMDBID string
	if err := pool.QueryRow(ctx, `SELECT tmdb_id FROM media_items WHERE content_id = $1`, targetContentID).Scan(&targetTMDBID); err != nil {
		t.Fatalf("load target tmdb id: %v", err)
	}
	if targetTMDBID != "" {
		t.Fatalf("target tmdb_id = %q, want unchanged", targetTMDBID)
	}
}
